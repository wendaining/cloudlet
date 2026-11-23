//go:build integration

package containers_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

func TestAdapterLifecycle(t *testing.T) {
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

	ownership := containers.Ownership{
		Environment: "integration",
		Project:     "project-" + rand.Text(),
		Resource:    "container-" + rand.Text(),
		Generation:  1,
	}
	spec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}

	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := adapter.Stop(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup stop: %v", err)
		}
		if err := adapter.Remove(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup remove: %v", err)
		}
		// Remove leftovers even when the adapter cleanup failed so the test does
		// not leak containers.
		if _, err := docker.ContainerInspect(cleanupCtx, created.ID, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})

	if _, err := adapter.Create(ctx, spec); !errors.Is(err, containers.ErrConflict) {
		t.Fatalf("duplicate create = %v, want ErrConflict", err)
	}
	assertCreatedContainer(t, ctx, docker, created, spec)

	observation, err := adapter.Start(ctx, ownership, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Running {
		t.Fatalf("started container is not running: %+v", observation)
	}
	port := waitForPort(t, ctx, adapter, ownership, created.ID, spec.Port)
	if !port.HostIP.IsLoopback() {
		t.Fatalf("published port is not loopback: %+v", port)
	}
	body := waitForHTTP(t, ctx, port.HostAddress())
	if !strings.Contains(body, "Welcome to nginx!") {
		t.Fatalf("unexpected HTTP response: %.200s", body)
	}

	other := ownership
	other.Project = "project-other"
	if _, err := adapter.Observe(ctx, other, created.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("observe with mismatched ownership = %v, want ErrNotOwned", err)
	}
	if err := adapter.Stop(ctx, other, created.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("stop with mismatched ownership = %v, want ErrNotOwned", err)
	}
	if err := adapter.Remove(ctx, other, created.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("remove with mismatched ownership = %v, want ErrNotOwned", err)
	}
	if current, err := adapter.Observe(ctx, ownership, created.ID); err != nil || !current.Running {
		t.Fatalf("mismatched operations changed the container: %+v, %v", current, err)
	}

	unmanaged, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Image:  image,
		Config: &container.Config{User: "101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = docker.ContainerRemove(cleanupCtx, unmanaged.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if err := adapter.Stop(ctx, ownership, unmanaged.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("stop unmanaged container = %v, want ErrNotOwned", err)
	}
	if err := adapter.Remove(ctx, ownership, unmanaged.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("remove unmanaged container = %v, want ErrNotOwned", err)
	}
	if _, err := docker.ContainerInspect(ctx, unmanaged.ID, client.ContainerInspectOptions{}); err != nil {
		t.Fatalf("unmanaged container was changed: %v", err)
	}

	impostor, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "foreign-" + created.Name,
		Config: &container.Config{
			Image:  spec.Image,
			User:   spec.User,
			Labels: ownershipLabels(spec.Ownership),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = docker.ContainerRemove(cleanupCtx, impostor.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := adapter.Start(ctx, ownership, impostor.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("start with mismatched container name = %v, want ErrNotOwned", err)
	}
	if _, err := adapter.Observe(ctx, ownership, impostor.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("observe with mismatched container name = %v, want ErrNotOwned", err)
	}
	if err := adapter.Stop(ctx, ownership, impostor.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("stop with mismatched container name = %v, want ErrNotOwned", err)
	}
	if err := adapter.Remove(ctx, ownership, impostor.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("remove with mismatched container name = %v, want ErrNotOwned", err)
	}
	impostorState, err := docker.ContainerInspect(ctx, impostor.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("mismatched-name container was changed: %v", err)
	}
	if impostorState.Container.State == nil || impostorState.Container.State.Running {
		t.Fatalf("mismatched-name container state changed: %+v", impostorState.Container.State)
	}

	if err := adapter.Stop(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	stopped, err := adapter.Observe(ctx, ownership, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Running || stopped.Status != string(container.StateExited) {
		t.Fatalf("stopped container state = %+v", stopped)
	}
	waitForConnectionFailure(t, ctx, port.HostAddress())

	if err := adapter.Remove(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Observe(ctx, ownership, created.ID); !errors.Is(err, containers.ErrNotFound) {
		t.Fatalf("observe removed container = %v, want ErrNotFound", err)
	}
	waitForPortRelease(t, ctx, port.HostAddress())
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
	} else if !cerrdefs.IsNotFound(err) {
		t.Fatalf("inspect image %s: %v", image, err)
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

func assertCreatedContainer(t *testing.T, ctx context.Context, docker *client.Client, created containers.Container, spec containers.Spec) {
	t.Helper()
	result, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	info := result.Container
	if info.Name != "/"+created.Name {
		t.Fatalf("container name = %q, want %q", info.Name, created.Name)
	}
	if info.Config == nil || info.HostConfig == nil {
		t.Fatal("inspect response is missing config or host config")
	}
	wantLabels := ownershipLabels(spec.Ownership)
	for key, want := range wantLabels {
		if got := info.Config.Labels[key]; got != want {
			t.Fatalf("label %s = %q, want %q", key, got, want)
		}
	}
	if info.HostConfig.NanoCPUs != spec.Limits.CPUMillis*1_000_000 {
		t.Fatalf("nano CPUs = %d", info.HostConfig.NanoCPUs)
	}
	if info.HostConfig.Memory != spec.Limits.MemoryBytes {
		t.Fatalf("memory = %d", info.HostConfig.Memory)
	}
	if info.HostConfig.PidsLimit == nil || *info.HostConfig.PidsLimit != spec.Limits.PIDs {
		t.Fatalf("pids limit = %v", info.HostConfig.PidsLimit)
	}
	var nofile *container.Ulimit
	for _, limit := range info.HostConfig.Ulimits {
		if limit.Name == "nofile" {
			nofile = limit
		}
	}
	if nofile == nil || nofile.Soft != spec.Limits.OpenFiles || nofile.Hard != spec.Limits.OpenFiles {
		t.Fatalf("nofile limit = %+v", nofile)
	}
}

func ownershipLabels(ownership containers.Ownership) map[string]string {
	return map[string]string{
		containers.LabelManaged:     "true",
		containers.LabelEnvironment: ownership.Environment,
		containers.LabelComponent:   containers.ComponentRuntime,
		containers.LabelProject:     ownership.Project,
		containers.LabelResource:    ownership.Resource,
		containers.LabelGeneration:  strconv.FormatUint(ownership.Generation, 10),
	}
}

func TestAdapterFind(t *testing.T) {
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

	ownership := containers.Ownership{
		Environment: "integration",
		Project:     "project-" + rand.Text(),
		Resource:    "find-" + rand.Text(),
		Generation:  1,
	}
	spec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}

	if observation, found, err := adapter.Find(ctx, ownership); err != nil || found {
		t.Fatalf("查找不存在的容器返回 %+v, %t, %v", observation, found, err)
	}

	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := adapter.Stop(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup stop: %v", err)
		}
		if err := adapter.Remove(cleanupCtx, ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup remove: %v", err)
		}
		if _, err := docker.ContainerInspect(cleanupCtx, created.ID, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})

	observation, found, err := adapter.Find(ctx, ownership)
	if err != nil || !found {
		t.Fatalf("查找已创建容器返回 %+v, %t, %v", observation, found, err)
	}
	if observation.ID != created.ID || observation.Name != created.Name || observation.Running ||
		observation.CPUMillis != spec.Limits.CPUMillis || observation.MemoryBytes != spec.Limits.MemoryBytes ||
		observation.PIDs != spec.Limits.PIDs || observation.OpenFiles != spec.Limits.OpenFiles {
		t.Fatalf("已创建容器观测错误: %+v", observation)
	}

	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	observation, found, err = adapter.Find(ctx, ownership)
	if err != nil || !found || !observation.Running {
		t.Fatalf("查找运行中容器返回 %+v, %t, %v", observation, found, err)
	}

	if err := adapter.Stop(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Remove(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	if observation, found, err := adapter.Find(ctx, ownership); err != nil || found {
		t.Fatalf("查找已删除容器返回 %+v, %t, %v", observation, found, err)
	}

	impostor, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: ownershipName(ownership),
		Config: &container.Config{
			Image:  image,
			User:   "101",
			Labels: map[string]string{containers.LabelManaged: "true"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = docker.ContainerRemove(cleanupCtx, impostor.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if observation, found, err := adapter.Find(ctx, ownership); !errors.Is(err, containers.ErrNotOwned) || found {
		t.Fatalf("查找归属不匹配的容器返回 %+v, %t, %v", observation, found, err)
	}

	invalid := ownership
	invalid.Generation = 0
	if _, _, err := adapter.Find(ctx, invalid); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("查找无效归属返回 %v，要求 fs.ErrInvalid", err)
	}
}

func ownershipName(ownership containers.Ownership) string {
	return fmt.Sprintf("cloudlet-%d-%s-%d-%s-%d-%s-%d",
		len(ownership.Environment), ownership.Environment,
		len(ownership.Project), ownership.Project,
		len(ownership.Resource), ownership.Resource,
		ownership.Generation)
}

func waitForPort(t *testing.T, ctx context.Context, adapter *containers.Adapter, ownership containers.Ownership, id string, containerPort uint16) containers.PublishedPort {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last containers.Observation
	for {
		observation, err := adapter.Observe(ctx, ownership, id)
		if err != nil {
			t.Fatalf("observe container: %v", err)
		}
		last = observation
		for _, port := range observation.Ports {
			if port.ContainerPort == containerPort && port.Protocol == "tcp" {
				return port
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("container did not publish %d/tcp: %+v", containerPort, last)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for published port: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForHTTP(t *testing.T, ctx context.Context, address string) string {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
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
			t.Fatalf("HTTP request to %s did not succeed: %v", address, lastErr)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for HTTP response: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForConnectionFailure(t *testing.T, ctx context.Context, address string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return
		}
		connection.Close()
		if time.Now().After(deadline) {
			t.Fatalf("port %s still accepts connections after stop", address)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for connection failure: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func waitForPortRelease(t *testing.T, ctx context.Context, address string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		listener, err := net.Listen("tcp", address)
		if err == nil {
			if closeErr := listener.Close(); closeErr != nil {
				t.Errorf("close released port listener: %v", closeErr)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("port %s was not released: %v", address, err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for port release: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
