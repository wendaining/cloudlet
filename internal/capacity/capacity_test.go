package capacity

import (
	"context"
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
	for name, request := range map[string]Request{
		"missing execution": missingExecution,
		"missing project":   missingProject,
		"missing host":      badHost,
		"invalid resources": badResources,
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.validate(); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效预留请求返回 %v，要求 fs.ErrInvalid", err)
			}
		})
	}
}

func TestRemainingResources(t *testing.T) {
	limit := Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}
	used := Resources{CPUMillis: 3000, MemoryBytes: 3 << 30, Instances: 2}
	if actual := remainingResources(limit, used); actual != (Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 2}) {
		t.Fatalf("剩余容量返回 %+v", actual)
	}
	overused := Resources{CPUMillis: 5000, MemoryBytes: 5 << 30, Instances: 5}
	if actual := remainingResources(limit, overused); actual != (Resources{}) {
		t.Fatalf("超用后的剩余容量返回 %+v，要求零值", actual)
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

func TestResolveExisting(t *testing.T) {
	executionID := uuid.New()
	projectID := uuid.New()
	request := Request{
		ExecutionID: executionID,
		HostID:      "host-1",
		ProjectID:   projectID,
		Resources:   Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
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
	reservation, created, err := resolveExisting(active, request)
	if err != nil || created || reservation.ExecutionID != executionID || reservation.State != Active {
		t.Fatalf("重复预留返回 %+v, %t, %v", reservation, created, err)
	}

	released := active
	released.State = stateReleased
	if _, _, err := resolveExisting(released, request); !errors.Is(err, ErrExecutionReleased) {
		t.Fatalf("已释放执行重复预留返回 %v，要求 ErrExecutionReleased", err)
	}
	releasedDifferent := released
	releasedDifferent.CPUMillis = request.Resources.CPUMillis + 1
	if _, _, err := resolveExisting(releasedDifferent, request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("已释放执行以不同请求预留返回 %v，要求 ErrIdempotencyConflict", err)
	}

	for name, existing := range map[string]reservationRow{
		"host":      {ExecutionID: executionID, HostID: "host-2", ProjectID: projectID, CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
		"project":   {ExecutionID: executionID, HostID: "host-1", ProjectID: uuid.New(), CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
		"resources": {ExecutionID: executionID, HostID: "host-1", ProjectID: projectID, CPUMillis: 1001, MemoryBytes: 1 << 30, Instances: 1, State: stateActive},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := resolveExisting(existing, request); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("不同请求复用执行标识返回 %v，要求 ErrIdempotencyConflict", err)
			}
		})
	}

	invalid := active
	invalid.State = "unknown"
	if _, _, err := resolveExisting(invalid, request); err == nil {
		t.Fatal("未知预留状态未返回错误")
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

func TestProjectUsageRejectsNilProject(t *testing.T) {
	store := &Store{}
	if _, err := store.ProjectUsage(context.Background(), uuid.Nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空项目标识查询容量返回 %v，要求 fs.ErrInvalid", err)
	}
}
