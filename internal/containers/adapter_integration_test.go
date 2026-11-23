//go:build integration

package containers_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
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
		Access:    containers.AccessPublished,
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

func TestAdapterNetworkAccess(t *testing.T) {
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

	networkName := createTestNetwork(ctx, t, docker)
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
		Access:    containers.AccessNetwork,
		Network:   networkName,
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
		if _, err := docker.ContainerInspect(cleanupCtx, created.ID, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})

	assertCreatedNetworkContainer(t, ctx, docker, created, spec)

	observation, err := adapter.Start(ctx, ownership, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Running {
		t.Fatalf("started container is not running: %+v", observation)
	}
	if len(observation.Ports) != 0 {
		t.Fatalf("network container published host ports: %+v", observation.Ports)
	}
	endpoint := waitForNetworkEndpoint(t, ctx, adapter, ownership, created.ID, networkName, spec.Port)
	if endpoint.Name == "" || !strings.HasPrefix(endpoint.Name, "cloudlet-") {
		t.Fatalf("network endpoint has no managed DNS name: %+v", endpoint)
	}
	if !endpoint.IP.IsValid() || endpoint.IP.IsLoopback() {
		t.Fatalf("network endpoint has an invalid address: %+v", endpoint)
	}

	body := requestFromNetwork(ctx, t, docker, networkName, image, endpoint.IPAddress())
	if !strings.Contains(body, "Welcome to nginx!") {
		t.Fatalf("unexpected HTTP response from %s: %.200s", endpoint.IPAddress(), body)
	}
	body = requestFromNetwork(ctx, t, docker, networkName, image, endpoint.Address())
	if !strings.Contains(body, "Welcome to nginx!") {
		t.Fatalf("unexpected HTTP response from %s: %.200s", endpoint.Address(), body)
	}
	assertRunningNetworkContainer(t, ctx, docker, created, networkName, endpoint)

	other := ownership
	other.Project = "project-other"
	if _, err := adapter.Observe(ctx, other, created.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("observe with mismatched ownership = %v, want ErrNotOwned", err)
	}
	if err := adapter.Stop(ctx, other, created.ID); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("stop with mismatched ownership = %v, want ErrNotOwned", err)
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
	if len(stopped.Endpoints) != 0 {
		t.Fatalf("stopped container still reports network endpoints: %+v", stopped.Endpoints)
	}

	if err := adapter.Remove(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Observe(ctx, ownership, created.ID); !errors.Is(err, containers.ErrNotFound) {
		t.Fatalf("observe removed container = %v, want ErrNotFound", err)
	}
	networkInfo, err := docker.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, found := networkInfo.Network.Containers[created.ID]; found {
		t.Fatalf("removed container is still attached to network %s", networkName)
	}
}

func TestAdapterCreateRejectsMissingNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
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
		Image:     httpImage(t),
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   "cloudlet-missing-" + strings.ToLower(rand.Text()),
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
	_, createErr := adapter.Create(ctx, spec)
	if !errors.Is(createErr, containers.ErrNetworkNotFound) {
		t.Fatalf("create with missing network = %v, want ErrNetworkNotFound", createErr)
	}
	if !strings.Contains(createErr.Error(), spec.Network) {
		t.Fatalf("create error %q does not name network %q", createErr, spec.Network)
	}
	if !cerrdefs.IsNotFound(createErr) {
		t.Fatalf("create error %v does not preserve the Docker not-found error", createErr)
	}
	filters := make(client.Filters).Add("label", containers.LabelResource+"="+ownership.Resource)
	list, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("missing network still created containers: %+v", list.Items)
	}
}

func TestAdapterCreateRejectsNetworkIDPrefix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}

	networkName := createTestNetwork(ctx, t, docker)
	networkInfo, err := docker.NetworkInspect(ctx, networkName, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Docker resolves a network ID prefix even though the adapter requires a
	// name. The mismatch must be reported as a missing network, not as an
	// invalid spec.
	prefix := networkInfo.Network.ID[:12]
	ownership := containers.Ownership{
		Environment: "integration",
		Project:     "project-" + rand.Text(),
		Resource:    "container-" + rand.Text(),
		Generation:  1,
	}
	spec := containers.Spec{
		Ownership: ownership,
		Image:     httpImage(t),
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   prefix,
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
	_, createErr := adapter.Create(ctx, spec)
	if !errors.Is(createErr, containers.ErrNetworkNotFound) {
		t.Fatalf("create with network ID prefix = %v, want ErrNetworkNotFound", createErr)
	}
	filters := make(client.Filters).Add("label", containers.LabelResource+"="+ownership.Resource)
	list, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("network ID prefix still created containers: %+v", list.Items)
	}
}

func TestAdapterStartReportsMissingNetwork(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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

	networkName := createTestNetwork(ctx, t, docker)
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
		Access:    containers.AccessNetwork,
		Network:   networkName,
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
		if _, err := docker.ContainerInspect(cleanupCtx, created.ID, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})

	// A created container does not keep its network alive. Removing the network
	// must surface as a network error, not as a missing container.
	if _, err := docker.NetworkRemove(ctx, networkName, client.NetworkRemoveOptions{}); err != nil {
		t.Fatalf("remove network %s: %v", networkName, err)
	}
	_, startErr := adapter.Start(ctx, ownership, created.ID)
	if !errors.Is(startErr, containers.ErrNetworkNotFound) {
		t.Fatalf("start after network removal = %v, want ErrNetworkNotFound", startErr)
	}
	if errors.Is(startErr, containers.ErrNotFound) {
		t.Fatalf("start after network removal = %v, must not be ErrNotFound", startErr)
	}
	observation, err := adapter.Observe(ctx, ownership, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Running || observation.Status != string(container.StateCreated) {
		t.Fatalf("container state after failed start = %+v, want created", observation)
	}
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
	if got := info.Config.Labels[containers.LabelPort]; got != strconv.Itoa(int(spec.Port)) {
		t.Fatalf("port label = %q, want %d", got, spec.Port)
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

func createTestNetwork(ctx context.Context, t *testing.T, docker *client.Client) string {
	t.Helper()
	name := "cloudlet-test-" + strings.ToLower(rand.Text())
	result, err := docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge"})
	if err != nil {
		t.Fatalf("create network %s: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if _, err := docker.NetworkRemove(cleanupCtx, result.ID, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove network %s: %v", name, err)
		}
	})
	return name
}

func assertCreatedNetworkContainer(t *testing.T, ctx context.Context, docker *client.Client, created containers.Container, spec containers.Spec) {
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
	if info.HostConfig.NetworkMode != container.NetworkMode(spec.Network) {
		t.Fatalf("network mode = %q, want %q", info.HostConfig.NetworkMode, spec.Network)
	}
	if len(info.HostConfig.PortBindings) != 0 {
		t.Fatalf("network container has port bindings: %+v", info.HostConfig.PortBindings)
	}
	if info.NetworkSettings == nil {
		t.Fatal("inspect response is missing network settings")
	}
	endpoint, found := info.NetworkSettings.Networks[spec.Network]
	if !found || endpoint == nil {
		t.Fatalf("container is not attached to network %s: %+v", spec.Network, info.NetworkSettings.Networks)
	}
	for port, bindings := range info.NetworkSettings.Ports {
		if len(bindings) != 0 {
			t.Fatalf("network container published %s: %+v", port, bindings)
		}
	}
	port := network.MustParsePort(strconv.Itoa(int(spec.Port)) + "/tcp")
	if _, found := info.Config.ExposedPorts[port]; !found {
		t.Fatalf("exposed ports = %v, want %s", info.Config.ExposedPorts, port)
	}
	if got := info.Config.Labels[containers.LabelPort]; got != strconv.Itoa(int(spec.Port)) {
		t.Fatalf("port label = %q, want %d", got, spec.Port)
	}
}

func assertRunningNetworkContainer(t *testing.T, ctx context.Context, docker *client.Client, created containers.Container, networkName string, endpoint containers.NetworkEndpoint) {
	t.Helper()
	result, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	info := result.Container
	if info.HostConfig == nil || info.NetworkSettings == nil {
		t.Fatal("inspect response is missing host or network settings")
	}
	if info.HostConfig.NetworkMode != container.NetworkMode(networkName) {
		t.Fatalf("network mode = %q, want %q", info.HostConfig.NetworkMode, networkName)
	}
	if len(info.HostConfig.PortBindings) != 0 {
		t.Fatalf("network container has port bindings: %+v", info.HostConfig.PortBindings)
	}
	for port, bindings := range info.NetworkSettings.Ports {
		if len(bindings) != 0 {
			t.Fatalf("network container published %s: %+v", port, bindings)
		}
	}
	settings := info.NetworkSettings.Networks[networkName]
	if settings == nil {
		t.Fatalf("container is not attached to network %s: %+v", networkName, info.NetworkSettings.Networks)
	}
	if settings.IPAddress != endpoint.IP {
		t.Fatalf("inspect IP address = %s, want %s", settings.IPAddress, endpoint.IP)
	}
	if !slices.Contains(settings.Aliases, endpoint.Name) && !slices.Contains(settings.DNSNames, endpoint.Name) {
		t.Fatalf("inspect aliases = %v, DNS names = %v, want %q", settings.Aliases, settings.DNSNames, endpoint.Name)
	}
	t.Logf("docker inspect: network_mode=%q ip=%s dns_names=%v host_port_bindings=%v", info.HostConfig.NetworkMode, settings.IPAddress, settings.DNSNames, info.HostConfig.PortBindings)
}

func waitForNetworkEndpoint(t *testing.T, ctx context.Context, adapter *containers.Adapter, ownership containers.Ownership, id, networkName string, containerPort uint16) containers.NetworkEndpoint {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last containers.Observation
	for {
		observation, err := adapter.Observe(ctx, ownership, id)
		if err != nil {
			t.Fatalf("observe container: %v", err)
		}
		last = observation
		for _, endpoint := range observation.Endpoints {
			if endpoint.Network == networkName && endpoint.ContainerPort == containerPort && endpoint.Protocol == "tcp" {
				return endpoint
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("container did not report an endpoint on %s port %d: %+v", networkName, containerPort, last)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for network endpoint: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func requestFromNetwork(ctx context.Context, t *testing.T, docker *client.Client, networkName, image, address string) string {
	t.Helper()
	script := fmt.Sprintf("i=0; while [ $i -lt 100 ]; do curl -fs http://%s/ && exit 0; i=$((i+1)); sleep 0.2; done; exit 1", address)
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Image: image,
		Config: &container.Config{
			User:       "101",
			Entrypoint: []string{"sh"},
			Cmd:        []string{"-c", script},
			Tty:        true,
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(networkName),
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyDisabled,
			},
		},
	})
	if err != nil {
		t.Fatalf("create network client for %s: %v", address, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start network client for %s: %v", address, err)
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			t.Fatalf("network client for %s exited with %d: %s", address, result.StatusCode, containerLogs(ctx, t, docker, created.ID))
		}
	case err := <-wait.Error:
		t.Fatalf("wait for network client for %s: %v", address, err)
	case <-ctx.Done():
		t.Fatalf("wait for network client for %s: %v", address, ctx.Err())
	}
	return containerLogs(ctx, t, docker, created.ID)
}

func containerLogs(ctx context.Context, t *testing.T, docker *client.Client, id string) string {
	t.Helper()
	logs, err := docker.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatalf("read logs of container %s: %v", id, err)
	}
	defer func() { _ = logs.Close() }()
	body, err := io.ReadAll(io.LimitReader(logs, 1<<20))
	if err != nil {
		t.Fatalf("read logs of container %s: %v", id, err)
	}
	return string(body)
}
