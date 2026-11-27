//go:build integration

package database_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
)

func TestContainerRunningVolumeIsUnique(t *testing.T) {
	ctx := t.Context()
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const email = "container-volume-owner@cloudlet.local"
	const password = "container-volume-integration-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner.ID, "Container volume integration")
	if err != nil {
		t.Fatal(err)
	}
	projectID := uuid.MustParse(project.ID)
	operationStore, err := operations.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	register := func(name string) operations.Operation {
		t.Helper()
		op, _, err := operationStore.Register(ctx, operations.Registration{
			ProjectID: projectID, Action: "container.create:" + name,
			IdempotencyKey: name, RequestFingerprint: name,
		})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	volumeName := "cloudlet-vol-unique-test"
	execution := func(name, hostname string, op operations.Operation) (database.Container, database.ContainerExecution) {
		t.Helper()
		container, execution, err := store.CreateContainerExecution(ctx, name, database.ContainerExecution{
			ID: uuid.New(), ProjectID: projectID, OperationID: op.ID,
			Image: "example.com/app@sha256:" + strings.Repeat("a", 64), ImageDigest: "sha256:" + strings.Repeat("a", 64),
			Port: 8080, Hostname: hostname, PathPrefix: "/", CPUMillis: 100, MemoryBytes: 64 << 20,
			PIDs: 64, OpenFiles: 128, UserName: "1000:1000", Command: []string{}, Env: []string{},
			HealthPath: "/", HealthTimeoutMS: 1000,
			VolumeName: volumeName, VolumeMountPath: "/data", VolumeDirectory: "state",
		})
		if err != nil {
			t.Fatal(err)
		}
		return container, execution
	}
	first, firstExecution := execution("first", "first.localhost", register("first"))
	if _, err := store.ActivateContainer(ctx, first, firstExecution, "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	second, secondExecution := execution("second", "second.localhost", register("second"))
	if _, err := store.ActivateContainer(ctx, second, secondExecution, "http://127.0.0.1:2"); err == nil {
		t.Fatal("a second running execution acquired the same data volume")
	}
	containerResult, executionResult, err := store.ContainerResult(ctx, projectID, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if containerResult.State != "pending" || executionResult.State != "pending" {
		t.Fatalf("second execution state = %s/%s, want pending/pending", containerResult.State, executionResult.State)
	}
}
