//go:build integration

package containers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

func TestAdapterProjectNetworkLifecycle(t *testing.T) {
	ctx, docker, adapter, image := newIntegrationHarness(t)
	ownership := newProjectOwnership()
	networkSpec := containers.NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}

	projectNetwork, err := adapter.EnsureNetwork(ctx, networkSpec)
	if err != nil {
		t.Fatal(err)
	}
	cleanupProjectNetwork(t, docker, adapter, networkSpec, projectNetwork)
	if projectNetwork.Name != networkSpec.Name() {
		t.Fatalf("network name = %q, want %q", projectNetwork.Name, networkSpec.Name())
	}
	if err := (containers.NetworkSpec{}).Validate(); err == nil {
		t.Fatal("empty network spec validated")
	}
	assertProjectNetwork(t, ctx, docker, projectNetwork, networkSpec)

	again, err := adapter.EnsureNetwork(ctx, networkSpec)
	if err != nil {
		t.Fatal(err)
	}
	if again != projectNetwork {
		t.Fatalf("second EnsureNetwork() = %+v, want %+v", again, projectNetwork)
	}
	found, ok, err := adapter.FindNetwork(ctx, networkSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found != projectNetwork {
		t.Fatalf("FindNetwork() = %+v, %v, want %+v, true", found, ok, projectNetwork)
	}

	spec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   projectNetwork.Name,
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
	cleanupContainer(t, docker, adapter, ownership, created.ID)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	waitForNetworkEndpoint(t, ctx, adapter, ownership, created.ID, projectNetwork.Name, spec.Port)

	// A network with an attached container must not be removed. The container
	// keeps its endpoint until it is removed, even after it is stopped.
	if err := adapter.RemoveNetwork(ctx, networkSpec); !errors.Is(err, containers.ErrNetworkInUse) {
		t.Fatalf("remove network with container = %v, want ErrNetworkInUse", err)
	}
	if _, err := docker.NetworkInspect(ctx, projectNetwork.ID, client.NetworkInspectOptions{}); err != nil {
		t.Fatalf("network was removed while in use: %v", err)
	}

	if err := adapter.Stop(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	// A stopped container remains attached to the network, so the network is
	// still in use until the container is removed.
	if err := adapter.RemoveNetwork(ctx, networkSpec); !errors.Is(err, containers.ErrNetworkInUse) {
		t.Fatalf("remove network with stopped container = %v, want ErrNetworkInUse", err)
	}
	if err := adapter.Remove(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := adapter.RemoveNetwork(ctx, networkSpec); err != nil {
		t.Fatal(err)
	}
	if _, err := docker.NetworkInspect(ctx, projectNetwork.ID, client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("network still exists after removal: %v", err)
	}
	if _, ok, err := adapter.FindNetwork(ctx, networkSpec); err != nil || ok {
		t.Fatalf("FindNetwork() after removal = %v, %v, want false, nil", ok, err)
	}
	if err := adapter.RemoveNetwork(ctx, networkSpec); !errors.Is(err, containers.ErrNetworkNotFound) {
		t.Fatalf("second remove = %v, want ErrNetworkNotFound", err)
	}

	recreated, err := adapter.EnsureNetwork(ctx, networkSpec)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID == projectNetwork.ID {
		t.Fatalf("recreated network reused ID %s", recreated.ID)
	}
}

func TestAdapterProjectNetworkIsolation(t *testing.T) {
	ctx, docker, adapter, image := newIntegrationHarness(t)
	ownershipA := newProjectOwnership()
	ownershipB := newProjectOwnership()
	networkSpecA := containers.NetworkSpec{Environment: ownershipA.Environment, Project: ownershipA.Project}
	networkSpecB := containers.NetworkSpec{Environment: ownershipB.Environment, Project: ownershipB.Project}
	networkA, err := adapter.EnsureNetwork(ctx, networkSpecA)
	if err != nil {
		t.Fatal(err)
	}
	cleanupProjectNetwork(t, docker, adapter, networkSpecA, networkA)
	networkB, err := adapter.EnsureNetwork(ctx, networkSpecB)
	if err != nil {
		t.Fatal(err)
	}
	cleanupProjectNetwork(t, docker, adapter, networkSpecB, networkB)
	if networkA.ID == networkB.ID {
		t.Fatalf("projects share network ID %s", networkA.ID)
	}

	containerA := startProjectContainer(t, ctx, docker, adapter, image, ownershipA, networkA)
	containerB := startProjectContainer(t, ctx, docker, adapter, image, ownershipB, networkB)
	endpointA := waitForNetworkEndpoint(t, ctx, adapter, ownershipA, containerA.ID, networkA.Name, 8080)
	endpointB := waitForNetworkEndpoint(t, ctx, adapter, ownershipB, containerB.ID, networkB.Name, 8080)

	// Peers on the same project network reach the service by IP and by the
	// stable managed DNS alias.
	for _, address := range []string{endpointA.IPAddress(), endpointA.Address()} {
		body := requestFromNetwork(ctx, t, docker, networkA.Name, image, address)
		if !strings.Contains(body, "Welcome to nginx!") {
			t.Fatalf("same-project response from %s: %.200s", address, body)
		}
	}

	// Peers on another project network cannot reach the service by IP or by the
	// managed alias.
	for _, address := range []string{endpointB.IPAddress(), endpointB.Address()} {
		runNetworkClientExpectFailure(ctx, t, docker, networkA.Name, image, address)
	}

	// A container owned by project A cannot join project B's network even
	// though the network name is a syntactically valid network reference.
	foreignOwnership := ownershipA
	foreignOwnership.Resource = "container-foreign-" + strings.ToLower(rand.Text())
	foreign := containers.Spec{
		Ownership: foreignOwnership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   networkB.Name,
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
	if _, err := adapter.Create(ctx, foreign); !errors.Is(err, containers.ErrNetworkNotOwned) {
		t.Fatalf("create on foreign network = %v, want ErrNetworkNotOwned", err)
	}
	assertNoContainers(t, ctx, docker, foreignOwnership)
}

func TestAdapterEnsureNetworkConcurrent(t *testing.T) {
	ctx, docker, adapter, _ := newIntegrationHarness(t)
	ownership := newProjectOwnership()
	spec := containers.NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}

	const calls = 4
	type result struct {
		network containers.Network
		err     error
	}
	results := make(chan result, calls)
	var group sync.WaitGroup
	for range calls {
		group.Add(1)
		go func() {
			defer group.Done()
			network, err := adapter.EnsureNetwork(ctx, spec)
			results <- result{network: network, err: err}
		}()
	}
	group.Wait()
	close(results)

	var first containers.Network
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent EnsureNetwork() = %v", item.err)
		}
		if first.ID == "" {
			first = item.network
			cleanupProjectNetwork(t, docker, adapter, spec, first)
			continue
		}
		if item.network != first {
			t.Fatalf("concurrent EnsureNetwork() returned %+v, want %+v", item.network, first)
		}
	}
	if first.ID == "" {
		t.Fatal("concurrent EnsureNetwork() returned no network")
	}
	assertProjectNetwork(t, ctx, docker, first, spec)
}

func TestAdapterProjectNetworkOwnership(t *testing.T) {
	ctx, docker, adapter, image := newIntegrationHarness(t)

	t.Run("unmanaged network", func(t *testing.T) {
		ownership := newProjectOwnership()
		spec := containers.NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}
		id := createManualNetwork(t, ctx, docker, spec.Name(), nil)
		assertNetworkRejected(t, ctx, docker, adapter, image, ownership, spec, id, containers.ErrNetworkNotOwned)
	})

	t.Run("foreign ownership labels", func(t *testing.T) {
		ownership := newProjectOwnership()
		other := newProjectOwnership()
		spec := containers.NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}
		id := createManualNetwork(t, ctx, docker, spec.Name(), networkLabels(other.Environment, other.Project))
		assertNetworkRejected(t, ctx, docker, adapter, image, ownership, spec, id, containers.ErrNetworkNotOwned)
	})

	t.Run("managed network under another name", func(t *testing.T) {
		ownership := newProjectOwnership()
		spec := containers.NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}
		id := createManualNetwork(t, ctx, docker, "cloudlet-net-manual-"+strings.ToLower(rand.Text()),
			networkLabels(spec.Environment, spec.Project))
		t.Cleanup(func() { removeNetworkByID(t, docker, id) })

		if _, err := adapter.EnsureNetwork(ctx, spec); !errors.Is(err, containers.ErrConflict) {
			t.Fatalf("EnsureNetwork() with another managed network = %v, want ErrConflict", err)
		}
		if _, err := docker.NetworkInspect(ctx, spec.Name(), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Fatalf("EnsureNetwork() created a second network: %v", err)
		}
	})
}

func assertNetworkRejected(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, image string, ownership containers.Ownership, spec containers.NetworkSpec, networkID string, want error) {
	t.Helper()
	t.Cleanup(func() { removeNetworkByID(t, docker, networkID) })

	if _, err := adapter.EnsureNetwork(ctx, spec); !errors.Is(err, want) {
		t.Fatalf("EnsureNetwork() = %v, want %v", err, want)
	}
	if _, _, err := adapter.FindNetwork(ctx, spec); !errors.Is(err, want) {
		t.Fatalf("FindNetwork() = %v, want %v", err, want)
	}
	if err := adapter.RemoveNetwork(ctx, spec); !errors.Is(err, want) {
		t.Fatalf("RemoveNetwork() = %v, want %v", err, want)
	}
	if _, err := docker.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{}); err != nil {
		t.Fatalf("rejected operation changed network %s: %v", networkID, err)
	}
	containerSpec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   spec.Name(),
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
	if _, err := adapter.Create(ctx, containerSpec); !errors.Is(err, want) {
		t.Fatalf("Create() on rejected network = %v, want %v", err, want)
	}
	assertNoContainers(t, ctx, docker, ownership)
}

func newIntegrationHarness(t *testing.T) (context.Context, *client.Client, *containers.Adapter, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
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
	return ctx, docker, adapter, image
}

func newProjectOwnership() containers.Ownership {
	return containers.Ownership{
		Environment: "integration",
		Project:     "project-" + strings.ToLower(rand.Text()),
		Resource:    "container-" + strings.ToLower(rand.Text()),
		Generation:  1,
	}
}

func networkLabels(environment, project string) map[string]string {
	return map[string]string{
		containers.LabelManaged:     "true",
		containers.LabelEnvironment: environment,
		containers.LabelComponent:   containers.ComponentRuntime,
		containers.LabelProject:     project,
		containers.LabelResource:    containers.NetworkResource,
	}
}

func assertProjectNetwork(t *testing.T, ctx context.Context, docker *client.Client, network containers.Network, spec containers.NetworkSpec) {
	t.Helper()
	result, err := docker.NetworkInspect(ctx, network.ID, client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	info := result.Network
	if info.Name != network.Name || info.Name != spec.Name() {
		t.Fatalf("network name = %q, want %q", info.Name, spec.Name())
	}
	if info.Driver != "bridge" || info.Scope != "local" || info.ConfigOnly {
		t.Fatalf("network shape = driver %q scope %q config-only %v", info.Driver, info.Scope, info.ConfigOnly)
	}
	for key, want := range networkLabels(spec.Environment, spec.Project) {
		if got := info.Labels[key]; got != want {
			t.Fatalf("network label %s = %q, want %q", key, got, want)
		}
	}
	t.Logf("docker inspect: network=%s id=%s driver=%s scope=%s labels=%v", info.Name, info.ID, info.Driver, info.Scope, info.Labels)
}

func startProjectContainer(t *testing.T, ctx context.Context, docker *client.Client, adapter *containers.Adapter, image string, ownership containers.Ownership, network containers.Network) containers.Container {
	t.Helper()
	spec := containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Port:      8080,
		Access:    containers.AccessNetwork,
		Network:   network.Name,
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
	cleanupContainer(t, docker, adapter, ownership, created.ID)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	return created
}

func cleanupProjectNetwork(t *testing.T, docker *client.Client, adapter *containers.Adapter, spec containers.NetworkSpec, network containers.Network) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := adapter.RemoveNetwork(cleanupCtx, spec); err != nil && !errors.Is(err, containers.ErrNetworkNotFound) {
			t.Errorf("cleanup network %s: %v", spec.Name(), err)
		}
		if _, err := docker.NetworkInspect(cleanupCtx, network.ID, client.NetworkInspectOptions{}); err == nil {
			if _, err := docker.NetworkRemove(cleanupCtx, network.ID, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Logf("force cleanup network %s: %v", spec.Name(), err)
			}
		}
	})
}

func cleanupContainer(t *testing.T, docker *client.Client, adapter *containers.Adapter, ownership containers.Ownership, id string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := adapter.Stop(cleanupCtx, ownership, id); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup stop %s: %v", id, err)
		}
		if err := adapter.Remove(cleanupCtx, ownership, id); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup remove %s: %v", id, err)
		}
		if _, err := docker.ContainerInspect(cleanupCtx, id, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})
}

func createManualNetwork(t *testing.T, ctx context.Context, docker *client.Client, name string, labels map[string]string) string {
	t.Helper()
	result, err := docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: labels})
	if err != nil {
		t.Fatalf("create manual network %s: %v", name, err)
	}
	return result.ID
}

func removeNetworkByID(t *testing.T, docker *client.Client, id string) {
	t.Helper()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := docker.NetworkRemove(cleanupCtx, id, client.NetworkRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove manual network %s: %v", id, err)
	}
}

func assertNoContainers(t *testing.T, ctx context.Context, docker *client.Client, ownership containers.Ownership) {
	t.Helper()
	filters := make(client.Filters).Add("label", containers.LabelResource+"="+ownership.Resource)
	list, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("rejected operation created containers: %+v", list.Items)
	}
}

func runNetworkClientExpectFailure(ctx context.Context, t *testing.T, docker *client.Client, networkName, image, address string) {
	t.Helper()
	script := fmt.Sprintf("curl -fsS --max-time 2 http://%s/ >/dev/null", address)
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
		t.Fatalf("create cross-project client for %s: %v", address, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start cross-project client for %s: %v", address, err)
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-wait.Result:
		if result.StatusCode == 0 {
			t.Fatalf("cross-project request to %s succeeded", address)
		}
		t.Logf("cross-project request to %s failed with exit code %d", address, result.StatusCode)
	case err := <-wait.Error:
		t.Fatalf("wait for cross-project client for %s: %v", address, err)
	case <-ctx.Done():
		t.Fatalf("wait for cross-project client for %s: %v", address, ctx.Err())
	}
}
