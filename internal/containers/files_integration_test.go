//go:build integration

package containers_test

import (
	"context"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

func TestAdapterRemovesContainerWhenFileInstallationFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := docker.Close(); err != nil {
			t.Error(err)
		}
	})
	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	ownership := containers.Ownership{Environment: "files", Project: uuid.NewString(), Resource: uuid.NewString(), Generation: 1}
	spec := containers.Spec{Ownership: ownership, Image: image, User: "101", Port: 8080, Limits: containers.Limits{CPUMillis: 500, MemoryBytes: 128 << 20, PIDs: 64, OpenFiles: 1024}, Files: []containers.File{{Path: "etc/passwd/worker.mjs", Data: []byte("export default {}")}}}
	created, createErr := adapter.Create(ctx, spec)
	if created.ID != "" {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanupCancel()
			if _, err := docker.ContainerRemove(cleanupCtx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		})
	}
	if createErr == nil || created.ID == "" {
		t.Fatalf("expected real file installation failure after creation: %+v %v", created, createErr)
	}
	if _, found, err := adapter.Find(ctx, ownership); err != nil || found {
		t.Fatalf("failed file installation left a container: found=%v error=%v", found, err)
	}
	if _, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("failed container still exists: %v", err)
	}
}
