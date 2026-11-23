package executions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
)

var (
	errCreate  = errors.New("create failed")
	errStart   = errors.New("start failed")
	errStop    = errors.New("stop failed")
	errRemove  = errors.New("remove failed")
	errRelease = errors.New("release failed")
	errFind    = errors.New("find failed")
)

type trace struct {
	mu    sync.Mutex
	steps []string
}

func (t *trace) add(step string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *trace) names() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.steps)
}

type releaseCall struct {
	projectID   uuid.UUID
	executionID uuid.UUID
}

type fakeReservations struct {
	mu          sync.Mutex
	trace       *trace
	reserveFunc func(context.Context, capacity.Request) (capacity.Reservation, bool, error)
	releaseFunc func(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error)
	getFunc     func(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error)

	reserveCalls []capacity.Request
	releaseCalls []releaseCall
	getCalls     []releaseCall
}

func (f *fakeReservations) Reserve(ctx context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
	f.mu.Lock()
	f.reserveCalls = append(f.reserveCalls, request)
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("reserve")
	}
	if f.reserveFunc != nil {
		return f.reserveFunc(ctx, request)
	}
	return capacity.Reservation{
		ExecutionID: request.ExecutionID,
		HostID:      request.HostID,
		ProjectID:   request.ProjectID,
		Resources:   request.Resources,
		State:       capacity.Active,
	}, true, nil
}

func (f *fakeReservations) Release(ctx context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
	f.mu.Lock()
	f.releaseCalls = append(f.releaseCalls, releaseCall{projectID: projectID, executionID: executionID})
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("release")
	}
	if f.releaseFunc != nil {
		return f.releaseFunc(ctx, projectID, executionID)
	}
	now := time.Now()
	return capacity.Reservation{
		ExecutionID: executionID,
		ProjectID:   projectID,
		State:       capacity.Released,
		ReleasedAt:  &now,
	}, nil
}

func (f *fakeReservations) GetReservation(ctx context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
	f.mu.Lock()
	f.getCalls = append(f.getCalls, releaseCall{projectID: projectID, executionID: executionID})
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("get")
	}
	if f.getFunc != nil {
		return f.getFunc(ctx, projectID, executionID)
	}
	return capacity.Reservation{
		ExecutionID: executionID,
		HostID:      "host-1",
		ProjectID:   projectID,
		State:       capacity.Active,
	}, nil
}

type containerCall struct {
	ownership containers.Ownership
	id        string
	spec      containers.Spec
}

type fakeContainers struct {
	mu          sync.Mutex
	trace       *trace
	createFunc  func(context.Context, containers.Spec) (containers.Container, error)
	startFunc   func(context.Context, containers.Ownership, string) (containers.Observation, error)
	stopFunc    func(context.Context, containers.Ownership, string) error
	removeFunc  func(context.Context, containers.Ownership, string) error
	findFunc    func(context.Context, containers.Ownership) (containers.Observation, bool, error)
	createCalls []containers.Spec
	startCalls  []containerCall
	stopCalls   []containerCall
	removeCalls []containerCall
	findCalls   []containers.Ownership
}

func (f *fakeContainers) Create(ctx context.Context, spec containers.Spec) (containers.Container, error) {
	f.mu.Lock()
	f.createCalls = append(f.createCalls, spec)
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("create")
	}
	if f.createFunc != nil {
		return f.createFunc(ctx, spec)
	}
	return containers.Container{ID: "container-1", Name: "container-1"}, nil
}

func (f *fakeContainers) Start(ctx context.Context, ownership containers.Ownership, id string) (containers.Observation, error) {
	f.mu.Lock()
	f.startCalls = append(f.startCalls, containerCall{ownership: ownership, id: id})
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("start")
	}
	if f.startFunc != nil {
		return f.startFunc(ctx, ownership, id)
	}
	return containers.Observation{ID: id, Name: "container-1", Running: true}, nil
}

func (f *fakeContainers) Stop(ctx context.Context, ownership containers.Ownership, id string) error {
	f.mu.Lock()
	f.stopCalls = append(f.stopCalls, containerCall{ownership: ownership, id: id})
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("stop")
	}
	if f.stopFunc != nil {
		return f.stopFunc(ctx, ownership, id)
	}
	return nil
}

func (f *fakeContainers) Remove(ctx context.Context, ownership containers.Ownership, id string) error {
	f.mu.Lock()
	f.removeCalls = append(f.removeCalls, containerCall{ownership: ownership, id: id})
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("remove")
	}
	if f.removeFunc != nil {
		return f.removeFunc(ctx, ownership, id)
	}
	return nil
}

func (f *fakeContainers) Find(ctx context.Context, ownership containers.Ownership) (containers.Observation, bool, error) {
	f.mu.Lock()
	f.findCalls = append(f.findCalls, ownership)
	f.mu.Unlock()
	if f.trace != nil {
		f.trace.add("find")
	}
	if f.findFunc != nil {
		return f.findFunc(ctx, ownership)
	}
	return containers.Observation{}, false, nil
}

func newFakes() (*fakeReservations, *fakeContainers, *trace) {
	trace := &trace{}
	return &fakeReservations{trace: trace}, &fakeContainers{trace: trace}, trace
}

func newTestService(t *testing.T, reservations Reservations, runtime Containers) *Service {
	t.Helper()
	service, err := NewService(reservations, runtime, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

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
		ContainerID: "container-1",
	}
}

func matchingObservation(id string, running bool) containers.Observation {
	spec := validStartRequest().Spec
	return containers.Observation{
		ID:          id,
		Name:        id,
		Image:       spec.Image,
		Running:     running,
		CPUMillis:   spec.Limits.CPUMillis,
		MemoryBytes: spec.Limits.MemoryBytes,
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(nil, &fakeContainers{}, "host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空容量存储返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&fakeReservations{}, nil, "host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空容器适配器返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&fakeReservations{}, &fakeContainers{}, ""); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("空主机标识返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&fakeReservations{}, &fakeContainers{}, " host-1"); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("带空格主机标识返回 %v，要求 fs.ErrInvalid", err)
	}
	if _, err := NewService(&fakeReservations{}, &fakeContainers{}, "host-1"); err != nil {
		t.Fatalf("有效依赖返回错误: %v", err)
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
			reservations, runtime, trace := newFakes()
			service := newTestService(t, reservations, runtime)
			request := validStartRequest()
			test.change(&request)
			if _, err := service.Start(context.Background(), request); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效启动请求返回 %v，要求 fs.ErrInvalid", err)
			}
			if steps := trace.names(); len(steps) != 0 {
				t.Fatalf("无效启动请求执行了操作: %v", steps)
			}
		})
	}
}

func TestStartAdmitsBeforeCreatingContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "start"}; !slices.Equal(steps, want) {
		t.Fatalf("启动顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.reserveCalls) != 1 {
		t.Fatalf("预留调用 %d 次，预期 1 次", len(reservations.reserveCalls))
	}
	reservationRequest := reservations.reserveCalls[0]
	if reservationRequest.ExecutionID != request.ExecutionID || reservationRequest.ProjectID != request.ProjectID ||
		reservationRequest.HostID != "host-1" {
		t.Fatalf("预留请求内容错误: %+v", reservationRequest)
	}
	wantResources := capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1}
	if reservationRequest.Resources != wantResources {
		t.Fatalf("预留资源为 %+v，预期 %+v", reservationRequest.Resources, wantResources)
	}
	if execution.Container.ID != "container-1" || execution.Observation.ID != "container-1" ||
		!execution.Observation.Running || execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("启动结果错误: %+v", execution)
	}
}

func TestStartRejectsInsufficientCapacity(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(context.Context, capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{}, false, fmt.Errorf("主机容量不足: %w", capacity.ErrInsufficientCapacity)
	}
	service := newTestService(t, reservations, runtime)

	if _, err := service.Start(context.Background(), validStartRequest()); !errors.Is(err, capacity.ErrInsufficientCapacity) {
		t.Fatalf("容量不足返回 %v，要求 ErrInsufficientCapacity", err)
	}
	if steps, want := trace.names(), []string{"reserve"}; !slices.Equal(steps, want) {
		t.Fatalf("容量不足执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.createCalls) != 0 || len(runtime.startCalls) != 0 {
		t.Fatal("容量不足仍然操作了容器")
	}
}

func TestStartReleasesReservationWhenCreateFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, errCreate
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, errCreate) {
		t.Fatalf("创建失败返回 %v，要求包含创建错误", err)
	}
	if execution.Container.ID != "" || execution.Reservation.ExecutionID != uuid.Nil {
		t.Fatalf("创建失败返回部分结果: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("创建失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
	released := reservations.releaseCalls[0]
	if released.projectID != request.ProjectID || released.executionID != request.ExecutionID {
		t.Fatalf("释放标识错误: %+v", released)
	}
	if len(runtime.startCalls) != 0 {
		t.Fatal("创建失败后仍然启动容器")
	}
}

func TestStartReleasesReservationWhenCreateConflictsWithoutContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, fmt.Errorf("create: %w", containers.ErrConflict)
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, containers.ErrConflict) {
		t.Fatalf("创建冲突返回 %v，要求包含 ErrConflict", err)
	}
	if execution.Container.ID != "" || execution.Reservation.ExecutionID != uuid.Nil {
		t.Fatalf("创建冲突且无容器时返回部分结果: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("创建冲突执行了操作 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
}

func TestStartAdoptsContainerOnCreateConflict(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, fmt.Errorf("create: %w", containers.ErrConflict)
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", true), true, nil
	}
	service := newTestService(t, reservations, runtime)

	execution, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "existing-1" || !execution.Observation.Running {
		t.Fatalf("创建冲突未复用已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("创建冲突复用顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("创建冲突复用容器后释放了容量预留")
	}
}

func TestStartAdoptsContainerWhenCreateResultIsUncertain(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, errCreate
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", true), true, nil
	}
	service := newTestService(t, reservations, runtime)

	execution, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "existing-1" || !execution.Observation.Running {
		t.Fatalf("创建结果不明时未复用已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("创建结果不明时顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("创建结果不明时释放了容量预留")
	}
}

func TestStartResumesContainerOnCreateConflict(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, fmt.Errorf("create: %w", containers.ErrConflict)
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", false), true, nil
	}
	service := newTestService(t, reservations, runtime)

	execution, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "existing-1" || !execution.Observation.Running {
		t.Fatalf("创建冲突未恢复已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find", "start"}; !slices.Equal(steps, want) {
		t.Fatalf("创建冲突恢复顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("创建冲突恢复容器后释放了容量预留")
	}
}

func TestStartKeepsReservationWhenConflictOwnershipMismatch(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, fmt.Errorf("create: %w", containers.ErrConflict)
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return containers.Observation{}, false, fmt.Errorf("find: %w", containers.ErrNotOwned)
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, containers.ErrConflict) || !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("归属不匹配返回 %v，要求同时包含冲突与归属错误", err)
	}
	if execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("归属不匹配未返回预留: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("归属不匹配执行了操作 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("归属不匹配释放了容量预留")
	}
}

func TestStartCleansUpAndReleasesWhenStartFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.startFunc = func(context.Context, containers.Ownership, string) (containers.Observation, error) {
		return containers.Observation{}, errStart
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, errStart) {
		t.Fatalf("启动失败返回 %v，要求包含启动错误", err)
	}
	if execution.Container.ID != "container-1" || execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("启动失败未返回可清理的执行信息: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "start", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("启动失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.stopCalls) != 1 || runtime.stopCalls[0].id != "container-1" {
		t.Fatalf("停止调用错误: %+v", runtime.stopCalls)
	}
	if len(runtime.removeCalls) != 1 || runtime.removeCalls[0].id != "container-1" {
		t.Fatalf("删除调用错误: %+v", runtime.removeCalls)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
}

func TestStartKeepsReservationWhenCleanupFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.startFunc = func(context.Context, containers.Ownership, string) (containers.Observation, error) {
		return containers.Observation{}, errStart
	}
	runtime.stopFunc = func(context.Context, containers.Ownership, string) error {
		return errStop
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	_, err := service.Start(context.Background(), request)
	if !errors.Is(err, errStart) || !errors.Is(err, errStop) {
		t.Fatalf("清理失败返回 %v，要求同时包含启动与停止错误", err)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "start", "stop"}; !slices.Equal(steps, want) {
		t.Fatalf("清理失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.removeCalls) != 0 {
		t.Fatal("停止失败后仍然删除容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("清理未确认时释放了容量预留")
	}
}

func TestStartKeepsReservationWhenRemoveFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.startFunc = func(context.Context, containers.Ownership, string) (containers.Observation, error) {
		return containers.Observation{}, errStart
	}
	runtime.removeFunc = func(context.Context, containers.Ownership, string) error {
		return errRemove
	}
	service := newTestService(t, reservations, runtime)

	_, err := service.Start(context.Background(), validStartRequest())
	if !errors.Is(err, errStart) || !errors.Is(err, errRemove) {
		t.Fatalf("删除失败返回 %v，要求同时包含启动与删除错误", err)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "start", "stop", "remove"}; !slices.Equal(steps, want) {
		t.Fatalf("删除失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("删除未确认时释放了容量预留")
	}
}

func TestStartKeepsReservationWhenReleaseFailsAfterCleanup(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.startFunc = func(context.Context, containers.Ownership, string) (containers.Observation, error) {
		return containers.Observation{}, errStart
	}
	reservations.releaseFunc = func(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error) {
		return capacity.Reservation{}, errRelease
	}
	service := newTestService(t, reservations, runtime)

	_, err := service.Start(context.Background(), validStartRequest())
	if !errors.Is(err, errStart) || !errors.Is(err, errRelease) {
		t.Fatalf("释放失败返回 %v，要求同时包含启动与释放错误", err)
	}
	if steps, want := trace.names(), []string{"reserve", "create", "start", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("释放失败处理顺序为 %v，预期 %v", steps, want)
	}
}

func TestStartAdoptsExistingRunningContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", true), true, nil
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "existing-1" || !execution.Observation.Running {
		t.Fatalf("未复用已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("复用顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.createCalls) != 0 || len(runtime.startCalls) != 0 {
		t.Fatal("已有运行容器时仍然创建或启动容器")
	}
}

func TestStartRejectsAdoptedContainerWithDifferentLimits(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		observation := matchingObservation("existing-1", true)
		observation.CPUMillis = 2000
		return observation, true, nil
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("资源上限不一致返回 %v，要求 ErrExecutionConflict", err)
	}
	if execution.Container.ID != "existing-1" || execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("资源上限不一致未返回已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("资源上限不一致执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.startCalls) != 0 || len(runtime.createCalls) != 0 {
		t.Fatal("资源上限不一致仍然启动或创建容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("资源上限不一致释放了容量预留")
	}
}

func TestStartRejectsAdoptedContainerWithDifferentImage(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		observation := matchingObservation("existing-1", true)
		observation.Image = "other:latest"
		return observation, true, nil
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, ErrExecutionConflict) {
		t.Fatalf("镜像不一致返回 %v，要求 ErrExecutionConflict", err)
	}
	if execution.Container.ID != "existing-1" || execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("镜像不一致未返回已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("镜像不一致执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.startCalls) != 0 || len(runtime.createCalls) != 0 {
		t.Fatal("镜像不一致仍然启动或创建容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("镜像不一致释放了容量预留")
	}
}

func TestStartResumesExistingStoppedContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", false), true, nil
	}
	service := newTestService(t, reservations, runtime)

	execution, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "existing-1" || !execution.Observation.Running {
		t.Fatalf("未恢复已有容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find", "start"}; !slices.Equal(steps, want) {
		t.Fatalf("恢复顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.createCalls) != 0 {
		t.Fatal("已有容器时重新创建了容器")
	}
}

func TestStartKeepsReservationWhenFindFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return containers.Observation{}, false, errFind
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, errFind) {
		t.Fatalf("查找失败返回 %v，要求包含查找错误", err)
	}
	if execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("查找失败未返回预留: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find"}; !slices.Equal(steps, want) {
		t.Fatalf("查找失败执行了操作 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("查找失败释放了容量预留")
	}
}

func TestStartKeepsReservationWhenResumeFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		return matchingObservation("existing-1", false), true, nil
	}
	runtime.startFunc = func(context.Context, containers.Ownership, string) (containers.Observation, error) {
		return containers.Observation{}, errStart
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	execution, err := service.Start(context.Background(), request)
	if !errors.Is(err, errStart) {
		t.Fatalf("恢复失败返回 %v，要求包含启动错误", err)
	}
	if execution.Container.ID != "existing-1" || execution.Reservation.ExecutionID != request.ExecutionID {
		t.Fatalf("恢复失败未返回可清理信息: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find", "start"}; !slices.Equal(steps, want) {
		t.Fatalf("恢复失败执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.stopCalls) != 0 || len(runtime.removeCalls) != 0 {
		t.Fatal("恢复已有容器失败时删除或停止了容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("恢复失败释放了容量预留")
	}
}

func TestStartCreatesWhenReservationExistsWithoutContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, false, nil
	}
	service := newTestService(t, reservations, runtime)

	execution, err := service.Start(context.Background(), validStartRequest())
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container.ID != "container-1" {
		t.Fatalf("未创建缺失的容器: %+v", execution)
	}
	if steps, want := trace.names(), []string{"reserve", "find", "create", "start"}; !slices.Equal(steps, want) {
		t.Fatalf("恢复创建顺序为 %v，预期 %v", steps, want)
	}
}

func TestStopReleasesOnlyAfterRemoval(t *testing.T) {
	reservations, runtime, trace := newFakes()
	service := newTestService(t, reservations, runtime)
	request := validStopRequest()

	if err := service.Stop(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if steps, want := trace.names(), []string{"get", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("停止顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.stopCalls) != 1 || runtime.stopCalls[0].id != "container-1" {
		t.Fatalf("停止调用错误: %+v", runtime.stopCalls)
	}
	if len(runtime.removeCalls) != 1 || runtime.removeCalls[0].id != "container-1" {
		t.Fatalf("删除调用错误: %+v", runtime.removeCalls)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
}

func TestStopKeepsReservationWhenStopFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.stopFunc = func(context.Context, containers.Ownership, string) error {
		return errStop
	}
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); !errors.Is(err, errStop) {
		t.Fatalf("停止失败返回 %v，要求包含停止错误", err)
	}
	if steps, want := trace.names(), []string{"get", "stop"}; !slices.Equal(steps, want) {
		t.Fatalf("停止失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(runtime.removeCalls) != 0 || len(reservations.releaseCalls) != 0 {
		t.Fatal("停止失败后删除容器或释放容量")
	}
}

func TestStopKeepsReservationWhenRemoveFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	runtime.removeFunc = func(context.Context, containers.Ownership, string) error {
		return errRemove
	}
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); !errors.Is(err, errRemove) {
		t.Fatalf("删除失败返回 %v，要求包含删除错误", err)
	}
	if steps, want := trace.names(), []string{"get", "stop", "remove"}; !slices.Equal(steps, want) {
		t.Fatalf("删除失败处理顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("删除失败后释放了容量预留")
	}
}

func TestStopKeepsReservationWhenReleaseFails(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.releaseFunc = func(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error) {
		return capacity.Reservation{}, errRelease
	}
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); !errors.Is(err, errRelease) {
		t.Fatalf("释放失败返回 %v，要求包含释放错误", err)
	}
	if steps, want := trace.names(), []string{"get", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("释放失败处理顺序为 %v，预期 %v", steps, want)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	reservations, runtime, trace := newFakes()
	state := capacity.Active
	reservations.getFunc = func(_ context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
		return capacity.Reservation{ExecutionID: executionID, HostID: "host-1", ProjectID: projectID, State: state}, nil
	}
	reservations.releaseFunc = func(_ context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
		state = capacity.Released
		return capacity.Reservation{ExecutionID: executionID, ProjectID: projectID, State: capacity.Released}, nil
	}
	var stopCalls, removeCalls atomic.Int32
	notFoundAfterFirst := func(counter *atomic.Int32) func(context.Context, containers.Ownership, string) error {
		return func(context.Context, containers.Ownership, string) error {
			if counter.Add(1) > 1 {
				return fmt.Errorf("container: %w", containers.ErrNotFound)
			}
			return nil
		}
	}
	runtime.stopFunc = notFoundAfterFirst(&stopCalls)
	runtime.removeFunc = notFoundAfterFirst(&removeCalls)
	service := newTestService(t, reservations, runtime)
	request := validStopRequest()

	for range 2 {
		if err := service.Stop(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if steps, want := trace.names(), []string{"get", "stop", "remove", "release", "get", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("重复停止顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 2 {
		t.Fatalf("释放调用 %d 次，预期 2 次", len(reservations.releaseCalls))
	}
}

func TestStopReleasesWhenContainerAlreadyMissing(t *testing.T) {
	reservations, runtime, trace := newFakes()
	notFound := func(context.Context, containers.Ownership, string) error {
		return fmt.Errorf("container: %w", containers.ErrNotFound)
	}
	runtime.stopFunc = notFound
	runtime.removeFunc = notFound
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); err != nil {
		t.Fatal(err)
	}
	if steps, want := trace.names(), []string{"get", "stop", "remove", "release"}; !slices.Equal(steps, want) {
		t.Fatalf("容器已缺失时顺序为 %v，预期 %v", steps, want)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
}

func TestStopRejectsUnknownReservationBeforeTouchingContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.getFunc = func(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error) {
		return capacity.Reservation{}, fmt.Errorf("missing: %w", capacity.ErrReservationNotFound)
	}
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); !errors.Is(err, capacity.ErrReservationNotFound) {
		t.Fatalf("未知预留返回 %v，要求 ErrReservationNotFound", err)
	}
	if steps, want := trace.names(), []string{"get"}; !slices.Equal(steps, want) {
		t.Fatalf("未知预留执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.findCalls) != 0 || len(runtime.stopCalls) != 0 || len(runtime.removeCalls) != 0 {
		t.Fatal("未知预留仍然操作了容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("未知预留仍然释放容量")
	}
}

func TestStopRejectsReservationFromAnotherHost(t *testing.T) {
	reservations, runtime, trace := newFakes()
	reservations.getFunc = func(_ context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
		return capacity.Reservation{ExecutionID: executionID, HostID: "host-2", ProjectID: projectID, State: capacity.Active}, nil
	}
	service := newTestService(t, reservations, runtime)

	if err := service.Stop(context.Background(), validStopRequest()); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("跨主机停止返回 %v，要求 fs.ErrInvalid", err)
	}
	if steps, want := trace.names(), []string{"get"}; !slices.Equal(steps, want) {
		t.Fatalf("跨主机停止执行了操作 %v，预期 %v", steps, want)
	}
	if len(runtime.stopCalls) != 0 || len(runtime.removeCalls) != 0 {
		t.Fatal("跨主机停止操作了容器")
	}
	if len(reservations.releaseCalls) != 0 {
		t.Fatal("跨主机停止释放了容量")
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
		{"missing container", func(r *StopRequest) { r.ContainerID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservations, runtime, trace := newFakes()
			service := newTestService(t, reservations, runtime)
			request := validStopRequest()
			test.change(&request)
			if err := service.Stop(context.Background(), request); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("无效停止请求返回 %v，要求 fs.ErrInvalid", err)
			}
			if steps := trace.names(); len(steps) != 0 {
				t.Fatalf("无效停止请求执行了操作: %v", steps)
			}
		})
	}
}

func TestConcurrentDuplicateStartCreatesOneContainer(t *testing.T) {
	reservations, runtime, trace := newFakes()
	var reserves atomic.Int32
	reservations.reserveFunc = func(_ context.Context, request capacity.Request) (capacity.Reservation, bool, error) {
		created := reserves.Add(1) == 1
		return capacity.Reservation{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			Resources:   request.Resources,
			State:       capacity.Active,
		}, created, nil
	}
	var containerCreated atomic.Bool
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		containerCreated.Store(true)
		return containers.Container{ID: "container-1", Name: "container-1"}, nil
	}
	runtime.findFunc = func(context.Context, containers.Ownership) (containers.Observation, bool, error) {
		if !containerCreated.Load() {
			return containers.Observation{}, false, nil
		}
		return matchingObservation("container-1", true), true, nil
	}
	service := newTestService(t, reservations, runtime)
	request := validStartRequest()

	const attempts = 4
	results := make([]Execution, attempts)
	errs := make([]error, attempts)
	start := make(chan struct{})
	var pending sync.WaitGroup
	for index := range results {
		pending.Go(func() {
			<-start
			results[index], errs[index] = service.Start(context.Background(), request)
		})
	}
	close(start)
	pending.Wait()

	for index, err := range errs {
		if err != nil {
			t.Fatalf("并发重复启动 %d 返回 %v", index, err)
		}
		if results[index].Container.ID != "container-1" {
			t.Fatalf("并发重复启动 %d 返回容器 %+v", index, results[index].Container)
		}
	}
	if got := len(runtime.createCalls); got != 1 {
		t.Fatalf("并发重复启动创建 %d 个容器，预期 1 个", got)
	}
	if got := len(runtime.startCalls); got != 1 {
		t.Fatalf("并发重复启动启动 %d 次，预期 1 次", got)
	}
	steps := trace.names()
	createCount, findCount := 0, 0
	for _, step := range steps {
		switch step {
		case "create":
			createCount++
		case "find":
			findCount++
		}
	}
	if createCount != 1 || findCount != attempts-1 {
		t.Fatalf("并发重复启动步骤为 %v，预期 1 次创建和 %d 次查找", steps, attempts-1)
	}
}

func TestStartReleasesWithDetachedContext(t *testing.T) {
	reservations, runtime, _ := newFakes()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var releaseContextErr error
	runtime.createFunc = func(context.Context, containers.Spec) (containers.Container, error) {
		return containers.Container{}, ctx.Err()
	}
	reservations.releaseFunc = func(releaseCtx context.Context, projectID, executionID uuid.UUID) (capacity.Reservation, error) {
		releaseContextErr = releaseCtx.Err()
		return capacity.Reservation{ExecutionID: executionID, ProjectID: projectID, State: capacity.Released}, nil
	}
	service := newTestService(t, reservations, runtime)

	if _, err := service.Start(ctx, validStartRequest()); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消上下文返回 %v，要求 context.Canceled", err)
	}
	if releaseContextErr != nil {
		t.Fatalf("释放使用已取消上下文: %v", releaseContextErr)
	}
	if len(reservations.releaseCalls) != 1 {
		t.Fatalf("释放调用 %d 次，预期 1 次", len(reservations.releaseCalls))
	}
}
