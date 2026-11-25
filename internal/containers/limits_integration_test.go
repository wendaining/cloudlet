//go:build integration

package containers_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

const (
	limitContainerPort = 8080
	limitContainerUser = "101"
)

const (
	memoryWorkloadBytes = 200_000_000
	cpuLimitWorkload    = `timeout -s KILL 5 sh -c 'while :; do :; done'; echo done`
	pidsLimitWorkload   = `i=0; while [ $i -lt 100 ]; do sleep 30 & i=$((i+1)); done; ` +
		`echo "started=$i"; kill $(jobs -p) 2>/dev/null; echo done`
	openFilesLimitWorkload = `echo "nofile=$(ulimit -n)"; cd /tmp; ` +
		`awk 'BEGIN{for(i=0;i<200;i++){f="/tmp/f" i; print "x" > f}; print "opened"}'`
)

var memoryLimitWorkload = fmt.Sprintf(
	`x=$(head -c %d /dev/zero | tr "\0" x); if [ ${#x} -eq %d ]; then echo "len=${#x}"; else echo "short=${#x}"; exit 3; fi`,
	memoryWorkloadBytes, memoryWorkloadBytes,
)

func TestProbeHostLimits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })

	host, err := containers.ProbeHostLimits(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	reportHostLimits(t, host)
	if host.OSType != "linux" {
		t.Fatalf("OSType = %q, want linux", host.OSType)
	}
	if host.ServerVersion == "" || host.KernelVersion == "" {
		t.Fatalf("host versions are incomplete: %+v", host)
	}
	if len(host.Limits) != 4 {
		t.Fatalf("limit entries = %d, want 4", len(host.Limits))
	}
	for _, kind := range []containers.LimitKind{
		containers.LimitCPU, containers.LimitMemory, containers.LimitPIDs, containers.LimitOpenFiles,
	} {
		var found bool
		for _, limit := range host.Limits {
			if limit.Kind == kind {
				found = true
				if limit.Detail == "" {
					t.Fatalf("limit %s has no detail", kind)
				}
			}
		}
		if !found {
			t.Fatalf("limit %s is missing", kind)
		}
	}
	if !host.Supports(containers.LimitOpenFiles) {
		t.Fatal("Linux host does not report nofile ulimit support")
	}
}

// TestAdapterLimitsAcceptance verifies the CPU, memory, PIDs and open-file
// limits with bounded workloads in real Docker containers. The host support
// report decides which limits can be exercised; unsupported limits are skipped
// with their Docker capability detail instead of failing the host.
func TestAdapterLimitsAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })

	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}

	host, err := containers.ProbeHostLimits(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	reportHostLimits(t, host)

	environment := "limits-" + rand.Text()
	t.Run("cpu", func(t *testing.T) {
		requireHostLimit(t, host, containers.LimitCPU)
		verifyCPULimit(t, ctx, docker, adapter, environment, image)
	})
	t.Run("memory", func(t *testing.T) {
		requireHostLimit(t, host, containers.LimitMemory)
		if !host.SwapLimit {
			t.Skip("宿主不支持交换限制，无法保证内存 OOM 结果")
		}
		verifyMemoryLimit(t, ctx, docker, adapter, environment, image)
	})
	t.Run("pids", func(t *testing.T) {
		requireHostLimit(t, host, containers.LimitPIDs)
		verifyPIDsLimit(t, ctx, docker, adapter, environment, image)
	})
	t.Run("open_files", func(t *testing.T) {
		requireHostLimit(t, host, containers.LimitOpenFiles)
		verifyOpenFilesLimit(t, ctx, docker, adapter, environment, image)
	})
	assertNoLimitContainers(t, ctx, docker, environment)
}

func reportHostLimits(t *testing.T, host containers.HostLimits) {
	t.Helper()
	t.Logf("宿主支持范围: os=%s/%s (%s) Docker=%s kernel=%s cgroup=%s/%s CPUs=%d memory=%d swap_limit=%t",
		host.OSType, host.Architecture, host.OperatingSystem, host.ServerVersion,
		host.KernelVersion, host.CgroupVersion, host.CgroupDriver, host.CPUs, host.MemoryBytes, host.SwapLimit)
	for _, limit := range host.Limits {
		t.Logf("限制 %s: supported=%t detail=%s", limit.Kind, limit.Supported, limit.Detail)
	}
}

func requireHostLimit(t *testing.T, host containers.HostLimits, kind containers.LimitKind) {
	t.Helper()
	if host.Supports(kind) {
		return
	}
	for _, limit := range host.Limits {
		if limit.Kind == kind {
			t.Skipf("宿主不支持 %s 限制: %s", kind, limit.Detail)
		}
	}
	t.Skipf("宿主支持范围没有报告 %s 限制", kind)
}

func verifyCPULimit(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string) {
	t.Helper()
	limits := containers.Limits{CPUMillis: 500, MemoryBytes: 512 << 20, PIDs: 128, OpenFiles: 1024}
	ownership, created := createLimitContainer(t, ctx, docker, adapter, environment, image, limits, cpuLimitWorkload)
	time.Sleep(2500 * time.Millisecond)
	stats := limitContainerStats(t, ctx, docker, created.ID)
	usage := stats.CPUStats.CPUUsage.TotalUsage
	throttled := stats.CPUStats.ThrottlingData.ThrottledPeriods
	t.Logf("CPU 限制 %d 毫核: usage=%dns throttled_periods=%d", limits.CPUMillis, usage, throttled)
	if usage < 300_000_000 || usage > 1_800_000_000 {
		t.Fatalf("CPU 用量 %d 纳秒不在 500 毫核约 2.5 秒负载的预期范围", usage)
	}
	if throttled == 0 {
		t.Fatal("CPU 配额没有产生节流周期")
	}
	waitForContainerExit(t, ctx, adapter, ownership, created.ID)
	removeLimitContainer(t, docker, adapter, ownership, created)
}

func verifyMemoryLimit(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string) {
	t.Helper()
	limits := containers.Limits{CPUMillis: 1000, MemoryBytes: 64 << 20, PIDs: 128, OpenFiles: 1024}
	ownership, created, observation := runLimitContainer(t, ctx, docker, adapter, environment, image, limits, memoryLimitWorkload)
	if observation.MemoryBytes != limits.MemoryBytes {
		t.Fatalf("observed memory = %d, want %d", observation.MemoryBytes, limits.MemoryBytes)
	}
	info := inspectLimitContainer(t, ctx, docker, created.ID)
	if info.State == nil {
		t.Fatal("memory limit container has no state")
	}
	if info.HostConfig == nil {
		t.Fatal("memory limit container has no host config")
	}
	if info.HostConfig.MemorySwap <= 0 {
		t.Skipf("宿主没有限制内存交换: MemorySwap=%d", info.HostConfig.MemorySwap)
	}
	if memoryWorkloadBytes <= info.HostConfig.MemorySwap {
		t.Fatalf("内存负载 %d 字节没有超过内存与交换上限 %d 字节", memoryWorkloadBytes, info.HostConfig.MemorySwap)
	}
	logs := demultiplexedLogs(t, ctx, docker, created.ID)
	if !info.State.OOMKilled && info.State.ExitCode != 137 {
		t.Fatalf("内存限制没有触发 OOM: exit=%d oom=%t logs=%q", info.State.ExitCode, info.State.OOMKilled, logs)
	}
	if strings.Contains(logs, fmt.Sprintf("len=%d", memoryWorkloadBytes)) {
		t.Fatalf("内存负载在 64 MiB 限制下完成分配: %q", logs)
	}
	removeLimitContainer(t, docker, adapter, ownership, created)

	control := limits
	control.MemoryBytes = 1 << 30
	controlOwnership, controlCreated, controlObservation := runLimitContainer(t, ctx, docker, adapter, environment, image, control, memoryLimitWorkload)
	if controlObservation.ExitCode != 0 {
		t.Fatalf("内存对照退出码 = %d, logs=%q", controlObservation.ExitCode, demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	if !strings.Contains(demultiplexedLogs(t, ctx, docker, controlCreated.ID), fmt.Sprintf("len=%d", memoryWorkloadBytes)) {
		t.Fatalf("内存对照没有完成分配: %q", demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	removeLimitContainer(t, docker, adapter, controlOwnership, controlCreated)
}

func verifyPIDsLimit(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string) {
	t.Helper()
	limits := containers.Limits{CPUMillis: 1000, MemoryBytes: 512 << 20, PIDs: 16, OpenFiles: 1024}
	ownership, created, observation := runLimitContainer(t, ctx, docker, adapter, environment, image, limits, pidsLimitWorkload)
	if observation.PIDs != limits.PIDs {
		t.Fatalf("observed pids = %d, want %d", observation.PIDs, limits.PIDs)
	}
	logs := demultiplexedLogs(t, ctx, docker, created.ID)
	if observation.ExitCode == 0 {
		t.Fatalf("进程负载在 %d 个 PIDs 限制下正常退出: %q", limits.PIDs, logs)
	}
	if strings.Contains(logs, "started=100") {
		t.Fatalf("进程负载在 16 个 PIDs 限制下创建了 100 个进程: %q", logs)
	}
	removeLimitContainer(t, docker, adapter, ownership, created)

	control := limits
	control.PIDs = 128
	controlOwnership, controlCreated, controlObservation := runLimitContainer(t, ctx, docker, adapter, environment, image, control, pidsLimitWorkload)
	if controlObservation.ExitCode != 0 {
		t.Fatalf("进程对照退出码 = %d, logs=%q", controlObservation.ExitCode, demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	if !strings.Contains(demultiplexedLogs(t, ctx, docker, controlCreated.ID), "started=100") {
		t.Fatalf("进程对照没有创建 100 个进程: %q", demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	removeLimitContainer(t, docker, adapter, controlOwnership, controlCreated)
}

func verifyOpenFilesLimit(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string) {
	t.Helper()
	limits := containers.Limits{CPUMillis: 1000, MemoryBytes: 512 << 20, PIDs: 128, OpenFiles: 64}
	ownership, created, observation := runLimitContainer(t, ctx, docker, adapter, environment, image, limits, openFilesLimitWorkload)
	if observation.OpenFiles != limits.OpenFiles {
		t.Fatalf("observed open files = %d, want %d", observation.OpenFiles, limits.OpenFiles)
	}
	logs := demultiplexedLogs(t, ctx, docker, created.ID)
	if observation.ExitCode == 0 {
		t.Fatalf("文件句柄负载在 %d 个限制下正常退出: %q", limits.OpenFiles, logs)
	}
	if !strings.Contains(logs, "nofile=64") {
		t.Fatalf("文件句柄负载没有报告 nofile=64: %q", logs)
	}
	if strings.Contains(logs, "opened") {
		t.Fatalf("文件句柄负载在 64 个限制下打开了 200 个文件: %q", logs)
	}
	removeLimitContainer(t, docker, adapter, ownership, created)

	control := limits
	control.OpenFiles = 1024
	controlOwnership, controlCreated, controlObservation := runLimitContainer(t, ctx, docker, adapter, environment, image, control, openFilesLimitWorkload)
	if controlObservation.ExitCode != 0 {
		t.Fatalf("文件句柄对照退出码 = %d, logs=%q", controlObservation.ExitCode, demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	if !strings.Contains(demultiplexedLogs(t, ctx, docker, controlCreated.ID), "opened") {
		t.Fatalf("文件句柄对照没有打开 200 个文件: %q", demultiplexedLogs(t, ctx, docker, controlCreated.ID))
	}
	removeLimitContainer(t, docker, adapter, controlOwnership, controlCreated)
}

func createLimitContainer(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string, limits containers.Limits, command string) (containers.Ownership, containers.Container) {
	t.Helper()
	ownership := containers.Ownership{
		Environment: environment,
		Project:     "quota-" + rand.Text(),
		Resource:    "quota-" + rand.Text(),
		Generation:  1,
	}
	spec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      limitContainerUser,
		Command:   []string{"sh", "-c", command},
		Port:      limitContainerPort,
		Access:    containers.AccessPublished,
		Limits:    limits,
	}
	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatalf("create limit container: %v", err)
	}
	t.Cleanup(func() { removeLimitContainer(t, docker, adapter, ownership, created) })
	assertCreatedContainer(t, ctx, docker, created, spec)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatalf("start limit container %s: %v", created.ID, err)
	}
	return ownership, created
}

func runLimitContainer(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, environment, image string, limits containers.Limits, command string) (containers.Ownership, containers.Container, containers.Observation) {
	t.Helper()
	ownership, created := createLimitContainer(t, ctx, docker, adapter, environment, image, limits, command)
	observation := waitForContainerExit(t, ctx, adapter, ownership, created.ID)
	return ownership, created, observation
}

func waitForContainerExit(t *testing.T, ctx context.Context, adapter *containers.Adapter, ownership containers.Ownership, id string) containers.Observation {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		observation, err := adapter.Observe(ctx, ownership, id)
		if err != nil {
			t.Fatalf("observe container %s: %v", id, err)
		}
		if !observation.Running {
			return observation
		}
		if time.Now().After(deadline) {
			t.Fatalf("container %s did not exit: %+v", id, observation)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for container %s: %v", id, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func removeLimitContainer(t *testing.T, docker *client.Client, adapter *containers.Adapter, ownership containers.Ownership, created containers.Container) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := adapter.Stop(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		t.Errorf("cleanup stop %s: %v", created.ID, err)
	}
	if err := adapter.Remove(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		t.Errorf("cleanup remove %s: %v", created.ID, err)
	}
	if _, err := docker.ContainerInspect(cleanupCtx, created.ID, client.ContainerInspectOptions{}); err == nil {
		_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		t.Errorf("container %s still exists after cleanup", created.ID)
	} else if !cerrdefs.IsNotFound(err) {
		t.Errorf("verify removal of %s: %v", created.ID, err)
	}
}

func assertNoLimitContainers(t *testing.T, ctx context.Context, docker *client.Client, environment string) {
	t.Helper()
	result, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", containers.LabelEnvironment+"="+environment),
	})
	if err != nil {
		t.Fatalf("list limit containers: %v", err)
	}
	if len(result.Items) != 0 {
		ids := make([]string, 0, len(result.Items))
		for _, item := range result.Items {
			ids = append(ids, item.ID)
		}
		t.Fatalf("配额验收遗留 %d 个容器: %v", len(result.Items), ids)
	}
}

func inspectLimitContainer(t *testing.T, ctx context.Context, docker *client.Client, id string) container.InspectResponse {
	t.Helper()
	result, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect container %s: %v", id, err)
	}
	return result.Container
}

func limitContainerStats(t *testing.T, ctx context.Context, docker *client.Client, id string) container.StatsResponse {
	t.Helper()
	result, err := docker.ContainerStats(ctx, id, client.ContainerStatsOptions{Stream: false})
	if err != nil {
		t.Fatalf("read stats of container %s: %v", id, err)
	}
	defer func() { _ = result.Body.Close() }()
	var stats container.StatsResponse
	if err := json.NewDecoder(result.Body).Decode(&stats); err != nil {
		t.Fatalf("decode stats of container %s: %v", id, err)
	}
	return stats
}

func demultiplexedLogs(t *testing.T, ctx context.Context, docker *client.Client, id string) string {
	t.Helper()
	reader, err := docker.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("read logs of container %s: %v", id, err)
	}
	defer func() { _ = reader.Close() }()
	var output bytes.Buffer
	if _, err := stdcopy.StdCopy(&output, &output, reader); err != nil {
		t.Fatalf("demultiplex logs of container %s: %v", id, err)
	}
	return output.String()
}
