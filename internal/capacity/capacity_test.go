package capacity

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestResourcesValidate(t *testing.T) {
	valid := Resources{CPUMillis: 500, MemoryBytes: 128 << 20, Instances: 1}
	if err := valid.validate(); err != nil {
		t.Fatalf("有效容量返回错误: %v", err)
	}
	for name, resources := range map[string]Resources{
		"zero CPU":           {CPUMillis: 0, MemoryBytes: 1, Instances: 1},
		"negative CPU":       {CPUMillis: -1, MemoryBytes: 1, Instances: 1},
		"zero memory":        {CPUMillis: 1, MemoryBytes: 0, Instances: 1},
		"negative memory":    {CPUMillis: 1, MemoryBytes: -1, Instances: 1},
		"zero instances":     {CPUMillis: 1, MemoryBytes: 1, Instances: 0},
		"negative instances": {CPUMillis: 1, MemoryBytes: 1, Instances: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := resources.validate(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效容量返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestRequestValidate(t *testing.T) {
	valid := Request{
		ExecutionID: uuid.New(),
		HostID:      "host-1",
		ProjectID:   uuid.New(),
		Resources:   Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		Binding: Binding{
			Environment: "integration",
			Project:     "project-1",
			Resource:    "resource-1",
			Generation:  1,
			Image:       "nginx:1.29.3-alpine",
		},
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("有效预留请求返回错误: %v", err)
	}

	missingExecution := valid
	missingExecution.ExecutionID = uuid.Nil
	missingProject := valid
	missingProject.ProjectID = uuid.Nil
	badHost := valid
	badHost.HostID = ""
	badResources := valid
	badResources.Resources.Instances = 0
	badBinding := valid
	badBinding.Binding.Resource = ""
	for name, request := range map[string]Request{
		"missing execution": missingExecution,
		"missing project":   missingProject,
		"missing host":      badHost,
		"invalid resources": badResources,
		"invalid binding":   badBinding,
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.validate(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效预留请求返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestCheckAvailable(t *testing.T) {
	limit := Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	used := Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 3}
	if err := checkAvailable("主机", "host-1", limit, used, Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}); err != nil {
		t.Fatalf("刚好用尽剩余容量返回错误: %v", err)
	}
	for name, requested := range map[string]Resources{
		"CPU":       {CPUMillis: 1001, MemoryBytes: 1, Instances: 1},
		"memory":    {CPUMillis: 1, MemoryBytes: (1 << 30) + 1, Instances: 1},
		"instances": {CPUMillis: 1, MemoryBytes: 1, Instances: 2},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkAvailable("主机", "host-1", limit, used, requested); !errors.Is(err, ErrInsufficientCapacity) {
				t.Fatalf("超出剩余容量返回 %v，要求 ErrInsufficientCapacity", err)
			}
		})
	}
	overused := Resources{CPUMillis: 5000, MemoryBytes: 5 << 30, Instances: 5}
	if err := checkAvailable("主机", "host-1", limit, overused, Resources{CPUMillis: 1, MemoryBytes: 1, Instances: 1}); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("已超用时申请返回 %v，要求 ErrInsufficientCapacity", err)
	}
}

func TestValidateExisting(t *testing.T) {
	executionID := uuid.New()
	projectID := uuid.New()
	binding := Binding{
		Environment: "integration",
		Project:     projectID.String(),
		Resource:    "resource-1",
		Generation:  1,
		Image:       "nginx:1.29.3-alpine",
	}
	request := Request{
		ExecutionID: executionID,
		HostID:      "host-1",
		ProjectID:   projectID,
		Resources:   Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
		Binding:     binding,
	}
	active := reservationRow{
		ExecutionID: executionID,
		HostID:      request.HostID,
		ProjectID:   projectID,
		CPUMillis:   request.Resources.CPUMillis,
		MemoryBytes: request.Resources.MemoryBytes,
		Instances:   request.Resources.Instances,
		State:       stateActive,
	}
	applyBinding(&active, binding)
	if err := validateExisting(&active, request); err != nil {
		t.Fatalf("匹配的已有预留返回错误: %v", err)
	}

	unbound := active
	unbound.ContainerEnvironment = nil
	unbound.ContainerProject = nil
	unbound.ContainerResource = nil
	unbound.ContainerGeneration = nil
	unbound.ContainerImage = nil
	if err := validateExisting(&unbound, request); err != nil {
		t.Fatalf("未绑定归属的活动预留返回错误: %v", err)
	}

	for name, existing := range map[string]reservationRow{
		"host":      {ExecutionID: executionID, HostID: "host-2", ProjectID: projectID, CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
		"project":   {ExecutionID: executionID, HostID: "host-1", ProjectID: uuid.New(), CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
		"resources": {ExecutionID: executionID, HostID: "host-1", ProjectID: projectID, CPUMillis: 1001, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExisting(&existing, request); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("不同请求复用执行标识返回 %v，要求 ErrIdempotencyConflict", err)
			}
		})
	}

	different := request
	different.Binding.Resource = "resource-2"
	if err := validateExisting(&active, different); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("不同容器归属复用执行标识返回 %v，要求 ErrIdempotencyConflict", err)
	}

	releasedUnbound := unbound
	releasedUnbound.State = stateReleased
	if err := validateExisting(&releasedUnbound, request); !errors.Is(err, ErrExecutionReleased) {
		t.Fatalf("已释放且未绑定归属的预留返回 %v，要求 ErrExecutionReleased", err)
	}
	releasedBound := active
	releasedBound.State = stateReleased
	if err := validateExisting(&releasedBound, request); err != nil {
		t.Fatalf("已释放但归属匹配的预留返回错误: %v", err)
	}
	if err := validateExisting(&releasedBound, different); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("已释放且归属不同返回 %v，要求 ErrIdempotencyConflict", err)
	}
}

func TestBindingValidate(t *testing.T) {
	valid := Binding{
		Environment: "integration",
		Project:     uuid.NewString(),
		Resource:    "resource-1",
		Generation:  1,
		Image:       "nginx:1.29.3-alpine",
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("有效容器归属返回错误: %v", err)
	}
	if err := (Binding{}).validate(); err != nil {
		t.Fatalf("空容器归属应允许: %v", err)
	}
	tests := []struct {
		name   string
		change func(*Binding)
	}{
		{"missing environment", func(b *Binding) { b.Environment = "" }},
		{"padded environment", func(b *Binding) { b.Environment = " integration" }},
		{"missing project", func(b *Binding) { b.Project = "" }},
		{"missing resource", func(b *Binding) { b.Resource = "" }},
		{"control character", func(b *Binding) { b.Resource = "resource\n" }},
		{"zero generation", func(b *Binding) { b.Generation = 0 }},
		{"missing image", func(b *Binding) { b.Image = "" }},
		{"image with whitespace", func(b *Binding) { b.Image = "nginx alpine" }},
		{"image with NUL", func(b *Binding) { b.Image = "nginx\x00" }},
		{"image too long", func(b *Binding) { b.Image = strings.Repeat("a", maxImageLength+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binding := valid
			test.change(&binding)
			if err := binding.validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效容器归属返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestValidateHostID(t *testing.T) {
	for _, hostID := range []string{"host-1", "feature.test", "环境"} {
		if err := validateHostID(hostID); err != nil {
			t.Fatalf("有效主机标识 %q 返回错误: %v", hostID, err)
		}
	}
	for name, hostID := range map[string]string{
		"empty":      "",
		"leading":    " host",
		"trailing":   "host ",
		"too long":   strings.Repeat("a", 65),
		"NUL":        "host\x00",
		"newline":    "host\n",
		"whitespace": " ",
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateHostID(hostID); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效主机标识返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestNewStoreRejectsNilDatabase(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("空数据库连接创建存储未返回错误")
	}
}
