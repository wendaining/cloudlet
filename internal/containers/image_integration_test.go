//go:build integration

package containers_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/registry"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

const testRegistryImage = "registry:2@sha256:a3d8aaa63ed8681a604f1dea0aa03f100d5895b6a58ace528858a7b332415373"

func TestAdapterPrepareImage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	registry := startTestRegistry(ctx, t, docker)

	repository := registry.address + "/cloudlet/image-test"
	tag := repository + ":test"
	importTestImage(ctx, t, docker, repository, "test")
	t.Cleanup(func() { removeTestImage(t, docker, tag) })
	pushTestImage(ctx, t, docker, tag)
	digestReference := pushedDigestReference(ctx, t, docker, repository, tag)
	t.Cleanup(func() { removeTestImage(t, docker, digestReference) })

	// Remove the image by ID so both the tag and the digest reference are
	// dropped on classic and containerd image stores.
	local, err := docker.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ImageRemove(ctx, local.ID, client.ImageRemoveOptions{Force: true, PruneChildren: true}); err != nil {
		t.Fatalf("remove local image: %v", err)
	}
	if _, err := docker.ImageInspect(ctx, digestReference); !cerrdefs.IsNotFound(err) {
		t.Fatalf("image is still cached after removal: %v", err)
	}

	// Concurrent preparation of the same digest must all succeed and agree on
	// the image identity.
	const workers = 4
	prepared := make([]containers.Image, workers)
	errs := make([]error, workers)
	var wait sync.WaitGroup
	for index := range prepared {
		wait.Add(1)
		go func() {
			defer wait.Done()
			prepared[index], errs[index] = adapter.PrepareImage(ctx, digestReference)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent PrepareImage %d: %v", index, err)
		}
		if prepared[index] != prepared[0] {
			t.Fatalf("concurrent image %d = %+v, want %+v", index, prepared[index], prepared[0])
		}
	}
	assertPreparedImage(t, ctx, docker, prepared[0], digestReference)

	// A digest that the registry does not serve must fail as a pull.
	missing := repository + "@sha256:" + strings.Repeat("0", 64)
	if _, err := adapter.PrepareImage(ctx, missing); !errors.Is(err, containers.ErrImagePull) {
		t.Fatalf("PrepareImage(missing digest) = %v, want ErrImagePull", err)
	} else if !cerrdefs.IsNotFound(err) {
		t.Fatalf("PrepareImage(missing digest) = %v, want not found", err)
	}

	// Stopping the registry proves that a prepared image is reused from the
	// local cache without contacting the registry.
	registry.stop(t)
	cached, err := adapter.PrepareImage(ctx, digestReference)
	if err != nil {
		t.Fatalf("PrepareImage() from cache: %v", err)
	}
	if cached != prepared[0] {
		t.Fatalf("cached image = %+v, want %+v", cached, prepared[0])
	}
}

func assertPreparedImage(t *testing.T, ctx context.Context, docker *client.Client, prepared containers.Image, reference string) {
	t.Helper()
	if prepared.Reference != reference {
		t.Fatalf("reference = %q, want %q", prepared.Reference, reference)
	}
	if prepared.ID == "" {
		t.Fatal("prepared image ID is empty")
	}
	if !strings.HasSuffix(reference, "@"+prepared.Digest) {
		t.Fatalf("digest %q does not identify %q", prepared.Digest, reference)
	}
	result, err := docker.ImageInspect(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != prepared.ID {
		t.Fatalf("Docker image ID = %q, prepared ID = %q", result.ID, prepared.ID)
	}
}

type testRegistry struct {
	docker  *client.Client
	id      string
	address string
	stopped bool
}

func startTestRegistry(ctx context.Context, t *testing.T, docker *client.Client) *testRegistry {
	t.Helper()
	ensureImage(ctx, t, docker, testRegistryImage)
	port := network.MustParsePort("5000/tcp")
	result, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: "cloudlet-registry-" + rand.Text(),
		Config: &container.Config{
			Image:        testRegistryImage,
			ExposedPorts: network.PortSet{port: {}},
		},
		HostConfig: &container.HostConfig{
			// HostPort "0" explicitly requests an ephemeral port. Docker
			// Desktop does not route the daemon's own push requests to a
			// registry published with an empty HostPort.
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := &testRegistry{docker: docker, id: result.ID}
	t.Cleanup(func() { registry.remove(t) })
	if _, err := docker.ContainerStart(ctx, result.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	registry.address = waitForRegistryPort(ctx, t, docker, result.ID)
	waitForHTTP(t, ctx, registry.address+"/v2")
	return registry
}

func (r *testRegistry) stop(t *testing.T) {
	t.Helper()
	if r.stopped {
		return
	}
	r.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	seconds := 10
	if _, err := r.docker.ContainerStop(ctx, r.id, client.ContainerStopOptions{Timeout: &seconds}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("stop test registry: %v", err)
	}
}

func (r *testRegistry) remove(t *testing.T) {
	t.Helper()
	r.stop(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := r.docker.ContainerRemove(ctx, r.id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove test registry: %v", err)
	}
}

func waitForRegistryPort(ctx context.Context, t *testing.T, docker *client.Client, id string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Container.NetworkSettings != nil {
			for _, bindings := range result.Container.NetworkSettings.Ports {
				for _, binding := range bindings {
					if binding.HostIP.IsValid() && binding.HostPort != "" {
						return net.JoinHostPort(binding.HostIP.String(), binding.HostPort)
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("test registry did not publish a loopback port")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for test registry port: %v", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func importTestImage(ctx context.Context, t *testing.T, docker *client.Client, repository, tag string) {
	t.Helper()
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	content := []byte("cloudlet image preparation test\n")
	if err := writer.WriteHeader(&tar.Header{
		Name:     "hello.txt",
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := docker.ImageImport(ctx, client.ImageImportSource{
		Source:     bytes.NewReader(archive.Bytes()),
		SourceName: "-",
	}, repository, client.ImageImportOptions{Tag: tag})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, result); err != nil {
		t.Fatal(err)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
}

func pushTestImage(ctx context.Context, t *testing.T, docker *client.Client, tag string) {
	t.Helper()
	// Some daemons reject an empty X-Registry-Auth header even for an
	// unauthenticated local registry. Send an encoded empty AuthConfig.
	auth, err := json.Marshal(registry.AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	response, err := docker.ImagePush(ctx, tag, client.ImagePushOptions{
		RegistryAuth: base64.URLEncoding.EncodeToString(auth),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Wait(ctx); err != nil {
		t.Fatal(err)
	}
}

func pushedDigestReference(ctx context.Context, t *testing.T, docker *client.Client, repository, tag string) string {
	t.Helper()
	result, err := docker.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}
	for _, repoDigest := range result.RepoDigests {
		if strings.HasPrefix(repoDigest, repository+"@") {
			return repoDigest
		}
	}
	t.Fatalf("image %s has no digest for %s: %v", tag, repository, result.RepoDigests)
	return ""
}

func removeTestImage(t *testing.T, docker *client.Client, reference string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := docker.ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: true, PruneChildren: true}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Errorf("remove prepared image: %v", err)
	}
}
