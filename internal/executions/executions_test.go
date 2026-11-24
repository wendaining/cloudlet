package executions

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
)

func validStartRequest() StartRequest {
	projectID := uuid.New()
	return StartRequest{
		ExecutionID: uuid.New(),
		ProjectID:   projectID,
		Spec: containers.Spec{
			Ownership: containers.Ownership{
				Environment: "test",
				Project:     projectID.String(),
				Resource:    "resource-1",
				Generation:  1,
			},
			Image: "nginx:1.29.3-alpine",
			User:  "101",
			Port:  8080,
			Limits: containers.Limits{
				CPUMillis:   1000,
				MemoryBytes: 1 << 30,
				PIDs:        64,
				OpenFiles:   1024,
			},
		},
	}
}

func validStopRequest() StopRequest {
	start := validStartRequest()
	return StopRequest{
		ExecutionID: start.ExecutionID,
		ProjectID:   start.ProjectID,
		Ownership:   start.Spec.Ownership,
		ContainerID: "0123456789abcdef",
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(nil, nil, "host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空容量存储返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&capacity.Store{}, nil, "host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空容器适配器返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&capacity.Store{}, &containers.Adapter{}, ""); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空主机标识返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&capacity.Store{}, &containers.Adapter{}, " host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("带空格主机标识返回 %v，要求 fs.ErrInvalid", err)
	}
}

func TestStartValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*StartRequest)
	}{
		{"missing execution", func(r *StartRequest) { r.ExecutionID = uuid.Nil }},
		{"missing project", func(r *StartRequest) { r.ProjectID = uuid.Nil }},
		{"invalid spec", func(r *StartRequest) { r.Spec.Port = 0 }},
		{"project mismatch", func(r *StartRequest) { r.Spec.Ownership.Project = "other-project" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validStartRequest()
			test.change(&request)
			if err := request.validate(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效启动请求返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestStopValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*StopRequest)
	}{
		{"missing execution", func(r *StopRequest) { r.ExecutionID = uuid.Nil }},
		{"missing project", func(r *StopRequest) { r.ProjectID = uuid.Nil }},
		{"invalid ownership", func(r *StopRequest) { r.Ownership.Generation = 0 }},
		{"project mismatch", func(r *StopRequest) { r.Ownership.Project = "other-project" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validStopRequest()
			test.change(&request)
			if err := request.validate(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效停止请求返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestStopValidationAllowsMissingContainerID(t *testing.T) {
	request := validStopRequest()
	request.ContainerID = ""
	if err := request.validate(); err != nil {
		t.Fatalf("未记录容器标识的停止请求返回错误: %v", err)
	}
}

func TestCheckAdoptedContainer(t *testing.T) {
	request := validStartRequest()
	observation := containers.Observation{
		ID:          "container-1",
		Name:        "container-1",
		Image:       request.Spec.Image,
		CPUMillis:   request.Spec.Limits.CPUMillis,
		MemoryBytes: request.Spec.Limits.MemoryBytes,
		PIDs:        request.Spec.Limits.PIDs,
		OpenFiles:   request.Spec.Limits.OpenFiles,
	}
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); err != nil {
		t.Fatalf("匹配的容器返回错误: %v", err)
	}
	observation.Image = "other:latest"
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("不同镜像返回 %v，要求 ErrExecutionConflict", err)
	}
	observation.Image = request.Spec.Image
	observation.CPUMillis = 2000
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("不同 CPU 上限返回 %v，要求 ErrExecutionConflict", err)
	}
	observation.CPUMillis = request.Spec.Limits.CPUMillis
	observation.PIDs = 128
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("不同进程上限返回 %v，要求 ErrExecutionConflict", err)
	}
}

func TestBindingMapping(t *testing.T) {
	request := validStartRequest()
	binding := bindingFor(request.Spec)
	if binding.Environment != request.Spec.Ownership.Environment ||
		binding.Project != request.Spec.Ownership.Project ||
		binding.Resource != request.Spec.Ownership.Resource ||
		binding.Generation != request.Spec.Ownership.Generation ||
		binding.Image != request.Spec.Image {
		t.Fatalf("容器归属映射错误: %+v", binding)
	}
	if !bindingMatchesOwnership(binding, request.Spec.Ownership) {
		t.Fatal("相同归属未匹配")
	}
	other := request.Spec.Ownership
	other.Resource = "resource-2"
	if bindingMatchesOwnership(binding, other) {
		t.Fatal("不同归属匹配成功")
	}
}

func TestAdoptionRequiresThePreparedFileDigest(t *testing.T) {
	request := validStartRequest()
	request.Spec.Files = []containers.File{{Path: "worker/worker.mjs", Data: []byte("export default {}")}}
	digest, err := request.Spec.FilesDigest()
	if err != nil {
		t.Fatal(err)
	}
	observation := containers.Observation{Image: request.Spec.Image, CPUMillis: request.Spec.Limits.CPUMillis, MemoryBytes: request.Spec.Limits.MemoryBytes, PIDs: request.Spec.Limits.PIDs, OpenFiles: request.Spec.Limits.OpenFiles, FilesDigest: digest}
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); err != nil {
		t.Fatal(err)
	}
	request.Spec.Files[0].Data = []byte("changed module")
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("changed prepared files: %v", err)
	}
	request.Spec.Files[0].Path = "../outside"
	if err := checkAdoptedContainer(request.ExecutionID, observation, request.Spec); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("invalid prepared file: %v", err)
	}
}
