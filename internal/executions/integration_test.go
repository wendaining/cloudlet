//go:build integration

package executions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
)

func TestContainerAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	databaseStore := startDatabase(t, ctx)
	owner := seedOwner(t, ctx, databaseStore)
	projectA := createProject(t, ctx, databaseStore, owner.ID, "容量准入项目 A")
	projectB := createProject(t, ctx, databaseStore, owner.ID, "容量准入项目 B")

	docker := newDockerClient(t)
	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	capacityStore, err := capacity.NewStore(databaseStore.DB)
	if err != nil {
		t.Fatal(err)
	}
	hostID := "admission-" + uuid.NewString()
	budget := capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	if _, err := capacityStore.SetHostCapacity(ctx, hostID, budget); err != nil {
		t.Fatal(err)
	}
	for _, projectID := range []uuid.UUID{projectA, projectB} {
		if _, err := capacityStore.SetProjectQuota(ctx, projectID, budget); err != nil {
			t.Fatal(err)
		}
	}
	service, err := executions.NewService(capacityStore, adapter, hostID)
	if err != nil {
		t.Fatal(err)
	}
	instance := "admission-" + uuid.NewString()[:8]

	t.Cleanup(func() { _ = docker.Close() })

	type reservedExecution struct {
		projectID   uuid.UUID
		executionID uuid.UUID
	}
	var reserved []reservedExecution
	var reservedMu sync.Mutex
	remember := func(projectID, executionID uuid.UUID) {
		reservedMu.Lock()
		defer reservedMu.Unlock()
		reserved = append(reserved, reservedExecution{projectID: projectID, executionID: executionID})
	}
	t.Cleanup(func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), time.Minute)
		defer releaseCancel()
		reservedMu.Lock()
		executionIDs := append([]reservedExecution(nil), reserved...)
		reservedMu.Unlock()
		for _, execution := range executionIDs {
			if _, err := capacityStore.Release(releaseCtx, execution.projectID, execution.executionID); err != nil &&
				!errors.Is(err, capacity.ErrReservationNotFound) && !errors.Is(err, capacity.ErrExecutionReleased) {
				t.Logf("cleanup release execution %s: %v", execution.executionID, err)
			}
		}
	})
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		removeManagedContainers(t, cleanupCtx, docker, instance)
	})

	preExecution := uuid.New()
	remember(projectA, preExecution)
	if _, created, err := capacityStore.Reserve(ctx, capacity.Request{
		ExecutionID: preExecution,
		HostID:      hostID,
		ProjectID:   projectA,
		Resources:   capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3},
	}); err != nil || !created {
		t.Fatalf("预占容量返回 %t, %v", created, err)
	}

	const attempts = 8
	type attempt struct {
		executionID uuid.UUID
		spec        containers.Spec
		execution   executions.Execution
		err         error
	}
	results := make([]attempt, attempts)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range results {
		results[index].executionID = uuid.New()
		remember(projectB, results[index].executionID)
		results[index].spec = httpSpec(instance, projectB, fmt.Sprintf("attempt-%d", index), image)
		pending.Go(func() {
			<-start
			results[index].execution, results[index].err = service.Start(ctx, executions.StartRequest{
				ExecutionID: results[index].executionID,
				ProjectID:   projectB,
				Spec:        results[index].spec,
			})
		})
	}
	close(start)
	pending.Wait()

	winner := -1
	for index, result := range results {
		if result.err == nil {
			if winner >= 0 {
				t.Fatalf("并发申请产生多个成功结果: 索引 %d 与 %d", winner, index)
			}
			winner = index
			continue
		}
		if !errors.Is(result.err, capacity.ErrInsufficientCapacity) {
			t.Fatalf("并发申请 %d 返回 %v", index, result.err)
		}
		if observation, found, err := adapter.Find(ctx, result.spec.Ownership); err != nil || found {
			t.Fatalf("超额申请 %d 创建了容器: %+v, %t, %v", index, observation, found, err)
		}
	}
	if winner < 0 {
		t.Fatal("并发申请没有成功结果")
	}
	t.Logf("并发准入结果: 成功 1 个，容量不足 %d 个", attempts-1)
	remember(projectB, results[winner].executionID)
	if !results[winner].execution.Observation.Running {
		t.Fatalf("成功申请的容器未运行: %+v", results[winner].execution)
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	})
	if items := listManaged(t, ctx, docker, instance, projectB); len(items) != 1 {
		t.Fatalf("并发申请后受管理容器数量为 %d，预期 1", len(items))
	}
	t.Logf("准入后主机占用: CPU %d 毫核，内存 %d 字节，实例 %d", 4000, int64(4<<30), 4)

	port := waitForPort(t, ctx, adapter, results[winner].spec.Ownership, results[winner].execution.Container.ID, 8080)
	if body := waitForHTTP(t, ctx, port.HostAddress()); !strings.Contains(body, "Welcome to nginx!") {
		t.Fatalf("容器响应不符合预期: %.200s", body)
	}

	// Stopping the container alone does not release capacity. The reservation
	// must stay active until removal is confirmed.
	if err := adapter.Stop(ctx, results[winner].spec.Ownership, results[winner].execution.Container.ID); err != nil {
		t.Fatal(err)
	}
	reservation, err := capacityStore.GetReservation(ctx, projectB, results[winner].executionID)
	if err != nil || reservation.State != capacity.Active {
		t.Fatalf("仅停止容器后预留为 %+v, %v，要求仍为 active", reservation, err)
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	})

	if err := service.Stop(ctx, executions.StopRequest{
		ExecutionID: results[winner].executionID,
		ProjectID:   projectB,
		Ownership:   results[winner].spec.Ownership,
		ContainerID: results[winner].execution.Container.ID,
	}); err != nil {
		t.Fatal(err)
	}
	if observation, found, err := adapter.Find(ctx, results[winner].spec.Ownership); err != nil || found {
		t.Fatalf("停止后仍可找到容器: %+v, %t, %v", observation, found, err)
	}
	if items := listManaged(t, ctx, docker, instance, projectB); len(items) != 0 {
		t.Fatalf("停止后仍存在 %d 个受管理容器", len(items))
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{},
	})
	t.Logf("确认删除后主机占用: CPU %d 毫核，内存 %d 字节，实例 %d", 3000, int64(3<<30), 3)

	// The released host capacity can be admitted again by another execution.
	nextID := uuid.New()
	remember(projectB, nextID)
	nextSpec := httpSpec(instance, projectB, "next", image)
	next, err := service.Start(ctx, executions.StartRequest{ExecutionID: nextID, ProjectID: projectB, Spec: nextSpec})
	if err != nil {
		t.Fatal(err)
	}
	if !next.Observation.Running {
		t.Fatalf("释放后重新申请的容器未运行: %+v", next)
	}
	if err := service.Stop(ctx, executions.StopRequest{
		ExecutionID: nextID,
		ProjectID:   projectB,
		Ownership:   nextSpec.Ownership,
		ContainerID: next.Container.ID,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := capacityStore.Release(ctx, projectA, preExecution); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{},
	})

	// A failed create releases the reservation because no container exists.
	invalidID := uuid.New()
	remember(projectB, invalidID)
	invalidSpec := httpSpec(instance, projectB, "invalid-image", image)
	invalidSpec.Image = "example.invalid/cloudlet-missing:latest"
	if _, err := service.Start(ctx, executions.StartRequest{ExecutionID: invalidID, ProjectID: projectB, Spec: invalidSpec}); err == nil {
		t.Fatal("无效镜像启动未返回错误")
	}
	reservation, err = capacityStore.GetReservation(ctx, projectB, invalidID)
	if err != nil || reservation.State != capacity.Released {
		t.Fatalf("创建失败后预留为 %+v, %v，要求 released", reservation, err)
	}
	if observation, found, err := adapter.Find(ctx, invalidSpec.Ownership); err != nil || found {
		t.Fatalf("创建失败后存在容器: %+v, %t, %v", observation, found, err)
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{},
	})

	// Concurrent retries for the same execution adopt one container and charge
	// capacity once.
	duplicateID := uuid.New()
	remember(projectB, duplicateID)
	duplicateSpec := httpSpec(instance, projectB, "duplicate", image)
	const duplicates = 4
	duplicateResults := make([]executions.Execution, duplicates)
	duplicateErrors := make([]error, duplicates)
	start = make(chan struct{})
	for index := range duplicateResults {
		pending.Go(func() {
			<-start
			duplicateResults[index], duplicateErrors[index] = service.Start(ctx, executions.StartRequest{
				ExecutionID: duplicateID,
				ProjectID:   projectB,
				Spec:        duplicateSpec,
			})
		})
	}
	close(start)
	pending.Wait()

	for index, err := range duplicateErrors {
		if err != nil {
			t.Fatalf("并发重复启动 %d 返回 %v", index, err)
		}
		if duplicateResults[index].Container.ID != duplicateResults[0].Container.ID {
			t.Fatalf("并发重复启动 %d 返回容器 %+v，预期 %+v", index, duplicateResults[index].Container, duplicateResults[0].Container)
		}
	}
	if items := listManaged(t, ctx, docker, instance, projectB); len(items) != 1 {
		t.Fatalf("并发重复启动后受管理容器数量为 %d，预期 1", len(items))
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	})
	if err := service.Stop(ctx, executions.StopRequest{
		ExecutionID: duplicateID,
		ProjectID:   projectB,
		Ownership:   duplicateSpec.Ownership,
		ContainerID: duplicateResults[0].Container.ID,
	}); err != nil {
		t.Fatal(err)
	}
	assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
		HostID:       hostID,
		HostLimit:    budget,
		HostUsed:     capacity.Resources{},
		ProjectID:    projectB,
		ProjectLimit: budget,
		ProjectUsed:  capacity.Resources{},
	})

	t.Run("retry with different ownership is rejected", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		original := httpSpec(instance, projectB, "binding-original", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: original})
		if err != nil {
			t.Fatal(err)
		}
		changed := httpSpec(instance, projectB, "binding-changed", image)
		if _, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: changed}); !errors.Is(err, capacity.ErrIdempotencyConflict) {
			t.Fatalf("同一执行更换归属返回 %v，要求 ErrIdempotencyConflict", err)
		}
		if observation, found, err := adapter.Find(ctx, changed.Ownership); err != nil || found {
			t.Fatalf("更换归属后创建了第二个容器: %+v, %t, %v", observation, found, err)
		}
		if items := listManaged(t, ctx, docker, instance, projectB); len(items) != 1 {
			t.Fatalf("更换归属后受管理容器数量为 %d，预期 1", len(items))
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
			ProjectID: projectB, ProjectLimit: budget,
			ProjectUsed: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		})
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: original.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("ownership is unique across executions", func(t *testing.T) {
		firstID, secondID := uuid.New(), uuid.New()
		remember(projectB, firstID)
		remember(projectB, secondID)
		spec := httpSpec(instance, projectB, "binding-unique", image)
		first, err := service.Start(ctx, executions.StartRequest{ExecutionID: firstID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Start(ctx, executions.StartRequest{ExecutionID: secondID, ProjectID: projectB, Spec: spec}); !errors.Is(err, capacity.ErrOwnershipConflict) {
			t.Fatalf("不同执行复用归属返回 %v，要求 ErrOwnershipConflict", err)
		}
		if items := listManaged(t, ctx, docker, instance, projectB); len(items) != 1 {
			t.Fatalf("复用归属后受管理容器数量为 %d，预期 1", len(items))
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: firstID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: first.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stop rejects a different container identifier", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		spec := httpSpec(instance, projectB, "binding-wrong-id", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: "ffffffffffff",
		}); !errors.Is(err, executions.ErrExecutionConflict) {
			t.Fatalf("错误容器标识返回 %v，要求 ErrExecutionConflict", err)
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
			ProjectID: projectB, ProjectLimit: budget,
			ProjectUsed: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		})
		if observation, found, err := adapter.Find(ctx, spec.Ownership); err != nil || !found || !observation.Running {
			t.Fatalf("错误标识停止后容器状态异常: %+v, %t, %v", observation, found, err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stop rejects a different ownership", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		spec := httpSpec(instance, projectB, "binding-stop-original", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		other := spec
		other.Ownership.Resource = "binding-stop-other"
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: other.Ownership, ContainerID: execution.Container.ID,
		}); !errors.Is(err, executions.ErrExecutionConflict) {
			t.Fatalf("错误归属停止返回 %v，要求 ErrExecutionConflict", err)
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
			ProjectID: projectB, ProjectLimit: budget,
			ProjectUsed: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		})
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stop confirms cleanup by ownership when the container is gone", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		spec := httpSpec(instance, projectB, "binding-gone", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := docker.ContainerRemove(ctx, execution.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatal(err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed: capacity.Resources{}, ProjectID: projectB,
			ProjectLimit: budget, ProjectUsed: capacity.Resources{},
		})
		if observation, found, err := adapter.Find(ctx, spec.Ownership); err != nil || found {
			t.Fatalf("清理后仍可找到容器: %+v, %t, %v", observation, found, err)
		}
	})

	t.Run("repeated stop does not touch a new execution with the same ownership", func(t *testing.T) {
		firstID, secondID := uuid.New(), uuid.New()
		remember(projectB, firstID)
		remember(projectB, secondID)
		spec := httpSpec(instance, projectB, "binding-reuse-stop", image)
		first, err := service.Start(ctx, executions.StartRequest{ExecutionID: firstID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: firstID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: first.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
		second, err := service.Start(ctx, executions.StartRequest{ExecutionID: secondID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatalf("释放后复用归属失败: %v", err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: firstID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: first.Container.ID,
		}); err != nil {
			t.Fatalf("重复停止旧执行返回 %v", err)
		}
		if observation, found, err := adapter.Find(ctx, spec.Ownership); err != nil || !found || !observation.Running {
			t.Fatalf("重复停止旧执行影响了新容器: %+v, %t, %v", observation, found, err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: secondID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: second.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stop rejects a container with a different binding configuration", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		spec := httpSpec(instance, projectB, "binding-mismatch", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := docker.ContainerRemove(ctx, execution.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatal(err)
		}
		replacementSpec := spec
		replacementSpec.Limits.CPUMillis = 500
		replacement, err := adapter.Create(ctx, replacementSpec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			_, _ = docker.ContainerRemove(cleanupCtx, replacement.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		})
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: execution.Container.ID,
		}); !errors.Is(err, executions.ErrExecutionConflict) {
			t.Fatalf("不同配置的绑定容器返回 %v，要求 ErrExecutionConflict", err)
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed:  capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
			ProjectID: projectB, ProjectLimit: budget,
			ProjectUsed: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		})
		if observation, found, err := adapter.Find(ctx, spec.Ownership); err != nil || !found {
			t.Fatalf("配置不匹配后替换容器丢失: %+v, %t, %v", observation, found, err)
		}
		if _, err := docker.ContainerRemove(ctx, replacement.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Fatal(err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: spec.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("start failure cleans up binding and capacity", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		spec := httpSpec(instance, projectB, "binding-start-failure", image)
		spec.User = "nonexistentuser"
		if _, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: spec}); err == nil {
			t.Fatal("无效用户启动未返回错误")
		}
		reservation, err := capacityStore.GetReservation(ctx, projectB, executionID)
		if err != nil || reservation.State != capacity.Released {
			t.Fatalf("启动失败后预留为 %+v, %v，要求 released", reservation, err)
		}
		if observation, found, err := adapter.Find(ctx, spec.Ownership); err != nil || found {
			t.Fatalf("启动失败后存在容器: %+v, %t, %v", observation, found, err)
		}
		assertUsage(t, ctx, capacityStore, hostID, projectB, capacity.Usage{
			HostID: hostID, HostLimit: budget,
			HostUsed: capacity.Resources{}, ProjectID: projectB,
			ProjectLimit: budget, ProjectUsed: capacity.Resources{},
		})

		reusedID := uuid.New()
		remember(projectB, reusedID)
		reusedSpec := httpSpec(instance, projectB, "binding-start-failure", image)
		reused, err := service.Start(ctx, executions.StartRequest{ExecutionID: reusedID, ProjectID: projectB, Spec: reusedSpec})
		if err != nil {
			t.Fatalf("释放后复用归属失败: %v", err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: reusedID, ProjectID: projectB,
			Ownership: reusedSpec.Ownership, ContainerID: reused.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("retry with a different image is rejected", func(t *testing.T) {
		executionID := uuid.New()
		remember(projectB, executionID)
		original := httpSpec(instance, projectB, "binding-image", image)
		execution, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: original})
		if err != nil {
			t.Fatal(err)
		}
		changed := original
		changed.Image = "example.invalid/other:latest"
		if _, err := service.Start(ctx, executions.StartRequest{ExecutionID: executionID, ProjectID: projectB, Spec: changed}); !errors.Is(err, capacity.ErrIdempotencyConflict) {
			t.Fatalf("同一执行更换镜像返回 %v，要求 ErrIdempotencyConflict", err)
		}
		if err := service.Stop(ctx, executions.StopRequest{
			ExecutionID: executionID, ProjectID: projectB,
			Ownership: original.Ownership, ContainerID: execution.Container.ID,
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func httpSpec(instance string, projectID uuid.UUID, resource, image string) containers.Spec {
	return containers.Spec{
		Ownership: containers.Ownership{
			Environment: instance,
			Project:     projectID.String(),
			Resource:    resource,
			Generation:  1,
		},
		Image: image,
		User:  "101",
		Port:  8080,
		Limits: containers.Limits{
			CPUMillis:   1000,
			MemoryBytes: 1 << 30,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
}

func startDatabase(t *testing.T, ctx context.Context) *database.Store {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	postgresContainer, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("admission-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if postgresContainer != nil {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := postgresContainer.Terminate(cleanupCtx); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connection, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.SQL.Close() })
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func seedOwner(t *testing.T, ctx context.Context, store *database.Store) database.User {
	t.Helper()
	const email = "admission@cloudlet.local"
	const password = "admission-integration-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func createProject(t *testing.T, ctx context.Context, store *database.Store, ownerID, name string) uuid.UUID {
	t.Helper()
	project, err := store.CreateProject(ctx, ownerID, name)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := uuid.Parse(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return projectID
}

func assertUsage(t *testing.T, ctx context.Context, store *capacity.Store, hostID string, projectID uuid.UUID, expected capacity.Usage) {
	t.Helper()
	actual, err := store.Usage(ctx, hostID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if actual.HostID != expected.HostID || actual.HostLimit != expected.HostLimit || actual.HostUsed != expected.HostUsed ||
		actual.ProjectID != expected.ProjectID || actual.ProjectLimit != expected.ProjectLimit || actual.ProjectUsed != expected.ProjectUsed {
		t.Fatalf("容量占用为 %+v，预期 %+v", actual, expected)
	}
}

func newDockerClient(t *testing.T) *client.Client {
	t.Helper()
	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	return docker
}

func httpImage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		ContainerHTTP string `json:"container_http"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	if images.ContainerHTTP == "" {
		t.Fatal("configs/images.json: container_http is required")
	}
	return images.ContainerHTTP
}

func ensureImage(ctx context.Context, t *testing.T, docker *client.Client, image string) {
	t.Helper()
	if _, err := docker.ImageInspect(ctx, image); err == nil {
		return
	}
	pullCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	response, err := docker.ImagePull(pullCtx, image, client.ImagePullOptions{})
	if err != nil {
		t.Fatalf("pull image %s: %v", image, err)
	}
	if err := response.Wait(pullCtx); err != nil {
		t.Fatalf("pull image %s: %v", image, err)
	}
}

func listManaged(t *testing.T, ctx context.Context, docker *client.Client, instance string, projectID uuid.UUID) []container.Summary {
	t.Helper()
	result, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", containers.LabelProject+"="+projectID.String()),
	})
	if err != nil {
		t.Fatal(err)
	}
	var items []container.Summary
	for _, item := range result.Items {
		if item.Labels[containers.LabelManaged] == "true" && item.Labels[containers.LabelEnvironment] == instance {
			items = append(items, item)
		}
	}
	return items
}

func removeManagedContainers(t *testing.T, ctx context.Context, docker *client.Client, instance string) {
	t.Helper()
	result, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", containers.LabelEnvironment+"="+instance),
	})
	if err != nil {
		t.Errorf("list containers for cleanup: %v", err)
		return
	}
	for _, item := range result.Items {
		if item.Labels[containers.LabelManaged] != "true" {
			continue
		}
		if _, err := docker.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			t.Errorf("remove container %s: %v", item.ID, err)
		}
	}
}

func waitForPort(t *testing.T, ctx context.Context, adapter *containers.Adapter, ownership containers.Ownership, id string, containerPort uint16) containers.PublishedPort {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		observation, found, err := adapter.Find(ctx, ownership)
		if err != nil {
			t.Fatalf("find container: %v", err)
		}
		if found && observation.ID == id {
			for _, port := range observation.Ports {
				if port.ContainerPort == containerPort && port.Protocol == "tcp" {
					return port
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("容器未发布 %d/tcp: %+v", containerPort, observation)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("等待发布端口: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForHTTP(t *testing.T, ctx context.Context, address string) string {
	t.Helper()
	httpClient := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpClient.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			response.Body.Close()
			switch {
			case readErr != nil:
				lastErr = readErr
			case response.StatusCode != http.StatusOK:
				lastErr = fmt.Errorf("status %d", response.StatusCode)
			default:
				return string(body)
			}
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			t.Fatalf("HTTP 请求 %s 未成功: %v", address, lastErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("等待 HTTP 响应: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
