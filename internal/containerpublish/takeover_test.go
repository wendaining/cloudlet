package containerpublish

import (
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestExecutionSpecRebuildsStoredConfiguration(t *testing.T) {
	service := &Service{instance: "development"}
	projectID := uuid.New()
	containerID := uuid.New()
	execution := database.ContainerExecution{
		ProjectID: projectID, ContainerID: containerID, Generation: 3,
		Image: "example.com/app@" + testDigest, Port: 8080,
		CPUMillis: 500, MemoryBytes: 128 << 20, PIDs: 256, OpenFiles: 1024,
		UserName: "101:101", Command: []string{"run"}, Env: []string{"A=1"},
		HealthPath: "/health", HealthTimeoutMS: 1500,
		VolumeName: "cloudlet-vol-test", VolumeMountPath: "/data", VolumeDirectory: "state",
	}
	bindingName := "business-db"
	secretVersionID := uuid.New()
	binding := postgresRuntimeBinding{
		name: &bindingName, secretVersionID: &secretVersionID,
		files: []containers.File{{Path: postgresURLPath, Data: []byte("postgres://business")}},
	}

	spec := service.executionSpec(execution, binding)
	if spec.Ownership != (containers.Ownership{Environment: "development", Project: projectID.String(), Resource: containerID.String(), Generation: 3}) {
		t.Fatalf("ownership = %+v", spec.Ownership)
	}
	if spec.Image != execution.Image || spec.User != "101:101" || spec.Port != 8080 || spec.Access != containers.AccessPublished {
		t.Fatalf("identity = %+v", spec)
	}
	if !slices.Equal(spec.Command, []string{"run"}) {
		t.Fatalf("command = %v", spec.Command)
	}
	if want := []string{"A=1", postgresURLFileEnv + "=/" + postgresURLPath}; !slices.Equal(spec.Env, want) {
		t.Fatalf("environment = %v, want %v", spec.Env, want)
	}
	if len(spec.Files) != 1 || spec.Files[0].Path != postgresURLPath || string(spec.Files[0].Data) != "postgres://business" {
		t.Fatalf("files = %+v", spec.Files)
	}
	if spec.DataVolume == nil || *spec.DataVolume != (containers.DataVolume{Name: "cloudlet-vol-test", MountPath: "/data", Directory: "state"}) {
		t.Fatalf("data volume = %+v", spec.DataVolume)
	}
	if spec.Limits != (containers.Limits{CPUMillis: 500, MemoryBytes: 128 << 20, PIDs: 256, OpenFiles: 1024}) {
		t.Fatalf("limits = %+v", spec.Limits)
	}
}

func TestExecutionSpecOmitsUnsetBindingAndVolume(t *testing.T) {
	service := &Service{instance: "development"}
	spec := service.executionSpec(database.ContainerExecution{
		ProjectID: uuid.New(), ContainerID: uuid.New(), Generation: 1,
		Image: "example.com/app@" + testDigest, Port: 8080,
		CPUMillis: 100, MemoryBytes: 64 << 20, PIDs: 64, OpenFiles: 128,
		UserName: "1000:1000",
	}, postgresRuntimeBinding{})
	if spec.DataVolume != nil || len(spec.Files) != 0 || len(spec.Env) != 0 {
		t.Fatalf("unset configuration = %+v", spec)
	}
}
