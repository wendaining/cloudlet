//go:build integration

package controlplane_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
)

func TestProjectCapacityAdjustment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	store := startCapacityAdjustmentDatabase(t, ctx)
	const ownerEmail = "capacity-adjust-owner@cloudlet.local"
	const developerEmail = "capacity-adjust-developer@cloudlet.local"
	const viewerEmail = "capacity-adjust-viewer@cloudlet.local"
	const outsiderEmail = "capacity-adjust-outsider@cloudlet.local"
	const password = "capacity-adjust-integration-password"
	for _, email := range []string{ownerEmail, developerEmail, viewerEmail, outsiderEmail} {
		if err := store.SeedUser(ctx, email, password); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := store.Authenticate(ctx, ownerEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	developer, err := store.Authenticate(ctx, developerEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Authenticate(ctx, viewerEmail, password)
	if err != nil {
		t.Fatal(err)
	}

	projectID := createProject(t, ctx, store, owner.ID, "额度调整项目")
	projectWithoutQuota := createProject(t, ctx, store, owner.ID, "额度调整未配置配额项目")
	if err := store.UpsertProjectMember(ctx, projectID.String(), developer.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, projectID.String(), viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}

	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	hostID := "capacity-adjust-" + uuid.NewString()
	if _, err := capacityStore.SetHostCapacity(ctx, hostID, capacity.Resources{CPUMillis: 8000, MemoryBytes: 8 << 30, Instances: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityStore.SetProjectQuota(ctx, projectID, capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}); err != nil {
		t.Fatal(err)
	}
	reservation, _, err := capacityStore.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler, cleanup := controlplane.NewHandler(store, config.Config{
		Instance: "integration", PublicOrigin: "http://localhost",
	}, slog.Default())
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()

	ownerClient := login(t, server, ownerEmail, password)
	developerClient := login(t, server, developerEmail, password)
	viewerClient := login(t, server, viewerEmail, password)
	outsiderClient := login(t, server, outsiderEmail, password)
	unauthenticatedClient := &http.Client{Timeout: 10 * time.Second}
	path := server.URL + "/api/v1/projects/" + projectID.String() + "/capacity"

	requestJSON(t, unauthenticatedClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 5000, MemoryBytes: 5 << 30, Instances: 5,
	}, http.StatusUnauthorized, nil)
	requestJSON(t, outsiderClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 5000, MemoryBytes: 5 << 30, Instances: 5,
	}, http.StatusNotFound, nil)
	requestJSON(t, developerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 5000, MemoryBytes: 5 << 30, Instances: 5,
	}, http.StatusForbidden, nil)
	requestJSON(t, viewerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 5000, MemoryBytes: 5 << 30, Instances: 5,
	}, http.StatusForbidden, nil)

	invalidBodies := map[string]any{
		"zero CPU":           map[string]any{"cpu_millis": 0, "memory_bytes": 1 << 30, "instances": 1},
		"negative CPU":       map[string]any{"cpu_millis": -1, "memory_bytes": 1 << 30, "instances": 1},
		"zero memory":        map[string]any{"cpu_millis": 1000, "memory_bytes": 0, "instances": 1},
		"negative memory":    map[string]any{"cpu_millis": 1000, "memory_bytes": -1, "instances": 1},
		"zero instances":     map[string]any{"cpu_millis": 1000, "memory_bytes": 1 << 30, "instances": 0},
		"negative instances": map[string]any{"cpu_millis": 1000, "memory_bytes": 1 << 30, "instances": -1},
		"missing CPU":        map[string]any{"memory_bytes": 1 << 30, "instances": 1},
		"missing memory":     map[string]any{"cpu_millis": 1000, "instances": 1},
		"missing instances":  map[string]any{"cpu_millis": 1000, "memory_bytes": 1 << 30},
		"decimal CPU":        map[string]any{"cpu_millis": 1.5, "memory_bytes": 1 << 30, "instances": 1},
		"string memory":      map[string]any{"cpu_millis": 1000, "memory_bytes": "1GiB", "instances": 1},
		"unknown field":      map[string]any{"cpu_millis": 1000, "memory_bytes": 1 << 30, "instances": 1, "unit": "core"},
	}
	for name, body := range invalidBodies {
		t.Run(name, func(t *testing.T) {
			requestJSON(t, ownerClient, http.MethodPatch, path, body, http.StatusBadRequest, nil)
		})
	}
	t.Run("trailing JSON object", func(t *testing.T) {
		requestRawJSON(t, ownerClient, http.MethodPatch, path, `{"cpu_millis":1000,"memory_bytes":1073741824,"instances":1}{}`, http.StatusBadRequest, nil)
	})
	unchanged, err := capacityStore.ProjectUsage(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Limit != (capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}) {
		t.Fatalf("非法请求改变了项目额度: %+v", unchanged)
	}

	expected := apigen.ProjectCapacity{
		ProjectId: projectID,
		Limit:     apigen.ResourceAmounts{CpuMillis: 6000, MemoryBytes: 6 << 30, Instances: 6},
		Used:      apigen.ResourceAmounts{CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 2},
		Remaining: apigen.ResourceAmounts{CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 4},
	}
	var updated apigen.ProjectCapacity
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 6000, MemoryBytes: 6 << 30, Instances: 6,
	}, http.StatusOK, &updated)
	if updated != expected {
		t.Fatalf("调高额度返回 %+v，预期 %+v", updated, expected)
	}

	var conflict apigen.Error
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 1,
	}, http.StatusConflict, &conflict)
	if !strings.Contains(conflict.Message, "当前占用") {
		t.Fatalf("额度冲突没有说明当前占用: %q", conflict.Message)
	}
	var afterConflict apigen.ProjectCapacity
	requestJSON(t, ownerClient, http.MethodGet, path, nil, http.StatusOK, &afterConflict)
	if afterConflict != expected {
		t.Fatalf("拒绝降低后额度变为 %+v，预期保持 %+v", afterConflict, expected)
	}
	active, err := capacityStore.GetReservation(ctx, projectID, reservation.ExecutionID)
	if err != nil || active.State != capacity.Active {
		t.Fatalf("拒绝降低后预留状态为 %+v, %v", active, err)
	}

	expected = apigen.ProjectCapacity{
		ProjectId: projectID,
		Limit:     apigen.ResourceAmounts{CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 2},
		Used:      apigen.ResourceAmounts{CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 2},
		Remaining: apigen.ResourceAmounts{},
	}
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 2,
	}, http.StatusOK, &updated)
	if updated != expected {
		t.Fatalf("额度等于占用返回 %+v，预期 %+v", updated, expected)
	}

	if _, err := capacityStore.Release(ctx, projectID, reservation.ExecutionID); err != nil {
		t.Fatal(err)
	}
	expected = apigen.ProjectCapacity{
		ProjectId: projectID,
		Limit:     apigen.ResourceAmounts{CpuMillis: 1, MemoryBytes: 1, Instances: 1},
		Used:      apigen.ResourceAmounts{},
		Remaining: apigen.ResourceAmounts{CpuMillis: 1, MemoryBytes: 1, Instances: 1},
	}
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 1, MemoryBytes: 1, Instances: 1,
	}, http.StatusOK, &updated)
	if updated != expected {
		t.Fatalf("释放后降低额度返回 %+v，预期 %+v", updated, expected)
	}

	withoutQuotaPath := server.URL + "/api/v1/projects/" + projectWithoutQuota.String() + "/capacity"
	requestJSON(t, ownerClient, http.MethodPatch, withoutQuotaPath, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2,
	}, http.StatusOK, &updated)
	if updated.Limit != (apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2}) {
		t.Fatalf("未配置配额项目调整返回 %+v", updated)
	}
	requestJSON(t, ownerClient, http.MethodGet, withoutQuotaPath, nil, http.StatusOK, &afterConflict)
	if afterConflict.Limit != (apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2}) {
		t.Fatalf("未配置配额项目调整后返回 %+v", afterConflict)
	}

	requestJSON(t, ownerClient, http.MethodPatch, server.URL+"/api/v1/projects/not-a-uuid/capacity", apigen.UpdateProjectCapacityRequest{
		CpuMillis: 1000, MemoryBytes: 1 << 30, Instances: 1,
	}, http.StatusNotFound, nil)
	requestJSON(t, ownerClient, http.MethodPatch, server.URL+"/api/v1/projects/"+uuid.NewString()+"/capacity", apigen.UpdateProjectCapacityRequest{
		CpuMillis: 1000, MemoryBytes: 1 << 30, Instances: 1,
	}, http.StatusNotFound, nil)
}

func TestProjectCapacityAdjustmentProtectsRunningContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	store := startCapacityAdjustmentDatabase(t, ctx)
	const ownerEmail = "capacity-container-owner@cloudlet.local"
	const password = "capacity-container-integration-password"
	if err := store.SeedUser(ctx, ownerEmail, password); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, ownerEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	projectID := createProject(t, ctx, store, owner.ID, "容器额度保护项目")

	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	hostID := "capacity-container-" + uuid.NewString()
	if _, err := capacityStore.SetHostCapacity(ctx, hostID, capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityStore.SetProjectQuota(ctx, projectID, capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}); err != nil {
		t.Fatal(err)
	}

	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	image := containerHTTPImage(t)
	ensureContainerImage(ctx, t, docker, image)
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	service, err := executions.NewService(capacityStore, adapter, hostID)
	if err != nil {
		t.Fatal(err)
	}

	instance := "capacity-adjust-" + uuid.NewString()[:8]
	ownership := containers.Ownership{
		Environment: instance,
		Project:     projectID.String(),
		Resource:    "web",
		Generation:  1,
	}
	spec := containers.Spec{
		Access:    containers.AccessPublished,
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Limits: containers.Limits{
			CPUMillis:   1000,
			MemoryBytes: 1 << 30,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
	executionID := uuid.New()
	execution, err := service.Start(ctx, executions.StartRequest{
		ExecutionID: executionID,
		ProjectID:   projectID,
		Spec:        spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := service.Stop(cleanupCtx, executions.StopRequest{
			ExecutionID: executionID,
			ProjectID:   projectID,
			Ownership:   ownership,
			ContainerID: execution.Container.ID,
		}); err != nil {
			t.Errorf("清理容器执行: %v", err)
		}
	})
	if !execution.Observation.Running {
		t.Fatalf("容器未运行: %+v", execution)
	}

	handler, cleanup := controlplane.NewHandler(store, config.Config{
		Instance: "integration", PublicOrigin: "http://localhost",
	}, slog.Default())
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()
	ownerClient := login(t, server, ownerEmail, password)
	path := server.URL + "/api/v1/projects/" + projectID.String() + "/capacity"

	var updated apigen.ProjectCapacity
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2,
	}, http.StatusOK, &updated)
	if updated.Limit != (apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2}) {
		t.Fatalf("调高额度返回 %+v", updated)
	}

	var conflict apigen.Error
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 500, MemoryBytes: 512 << 20, Instances: 1,
	}, http.StatusConflict, &conflict)
	if conflict.Message == "" {
		t.Fatal("额度冲突没有错误说明")
	}
	var afterConflict apigen.ProjectCapacity
	requestJSON(t, ownerClient, http.MethodGet, path, nil, http.StatusOK, &afterConflict)
	if afterConflict.Limit != (apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 2}) {
		t.Fatalf("拒绝降低后额度变为 %+v", afterConflict)
	}
	observation, err := adapter.Observe(ctx, ownership, execution.Container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Running {
		t.Fatalf("拒绝降低额度后容器停止运行: %+v", observation)
	}

	if err := service.Stop(ctx, executions.StopRequest{
		ExecutionID: executionID,
		ProjectID:   projectID,
		Ownership:   ownership,
		ContainerID: execution.Container.ID,
	}); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, ownerClient, http.MethodPatch, path, apigen.UpdateProjectCapacityRequest{
		CpuMillis: 1, MemoryBytes: 1, Instances: 1,
	}, http.StatusOK, &updated)
}

func startCapacityAdjustmentDatabase(t *testing.T, ctx context.Context) *database.Store {
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
	container, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("capacity-adjust-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connection, err := container.ConnectionString(ctx, "sslmode=disable")
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

func containerHTTPImage(t *testing.T) string {
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

func ensureContainerImage(ctx context.Context, t *testing.T, docker *client.Client, image string) {
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

func requestRawJSON(t *testing.T, client *http.Client, method, endpoint, body string, expected int, target any) {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		t.Fatalf("%s %s 返回 %d，要求 %d", method, endpoint, response.StatusCode, expected)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}
