// Package executions starts and stops managed containers under transactional
// host and project capacity reservations.
//
// Start reserves capacity before any Docker operation. A failed create is
// checked by deterministic name; if no container exists the reservation is
// released, otherwise the existing container is adopted after verifying its
// image and resource limits. A failed start stops
// and removes the created container, then releases capacity only after removal
// is confirmed. If cleanup or release fails, the reservation stays active so
// the platform cannot overcommit. Stop releases capacity only after the
// container is confirmed removed.
package executions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
)

const cleanupTimeout = 30 * time.Second

// ErrExecutionConflict reports that an active execution already has a
// container with a different image or resource limits than the request.
var ErrExecutionConflict = errors.New("执行已有不同的容器配置")

// Reservations is the capacity store used to admit an execution.
type Reservations interface {
	Reserve(context.Context, capacity.Request) (capacity.Reservation, bool, error)
	Release(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error)
	GetReservation(context.Context, uuid.UUID, uuid.UUID) (capacity.Reservation, error)
}

// Containers is the Docker adapter used to run an execution.
type Containers interface {
	Create(context.Context, containers.Spec) (containers.Container, error)
	Start(context.Context, containers.Ownership, string) (containers.Observation, error)
	Stop(context.Context, containers.Ownership, string) error
	Remove(context.Context, containers.Ownership, string) error
	Find(context.Context, containers.Ownership) (containers.Observation, bool, error)
}

// Service admits and runs containers for one host budget. It is safe for
// concurrent use.
type Service struct {
	reservations Reservations
	containers   Containers
	hostID       string

	locksMu sync.Mutex
	locks   map[uuid.UUID]*executionLock
}

type executionLock struct {
	mu   sync.Mutex
	refs int
}

// NewService requires a capacity store, a container adapter and the host
// identifier whose budget is charged for every execution.
func NewService(reservations Reservations, containerRuntime Containers, hostID string) (*Service, error) {
	if reservations == nil {
		return nil, fmt.Errorf("create execution service: capacity store is required: %w", fs.ErrInvalid)
	}
	if containerRuntime == nil {
		return nil, fmt.Errorf("create execution service: container adapter is required: %w", fs.ErrInvalid)
	}
	if err := capacity.ValidateHostID(hostID); err != nil {
		return nil, fmt.Errorf("create execution service: %w", err)
	}
	return &Service{
		reservations: reservations,
		containers:   containerRuntime,
		hostID:       hostID,
		locks:        make(map[uuid.UUID]*executionLock),
	}, nil
}

// StartRequest asks to admit and start one container execution. The ownership
// project must equal ProjectID so container labels and the capacity reservation
// identify the same project. Ownership must uniquely identify the execution;
// reusing the same environment, project, resource and generation for a different
// execution identifier can adopt the same container.
type StartRequest struct {
	ExecutionID uuid.UUID
	ProjectID   uuid.UUID
	Spec        containers.Spec
}

func (r StartRequest) validate() error {
	if r.ExecutionID == uuid.Nil {
		return fmt.Errorf("启动执行需要执行标识: %w", fs.ErrInvalid)
	}
	if r.ProjectID == uuid.Nil {
		return fmt.Errorf("启动执行需要项目标识: %w", fs.ErrInvalid)
	}
	if err := r.Spec.Validate(); err != nil {
		return err
	}
	if r.Spec.Ownership.Project != r.ProjectID.String() {
		return fmt.Errorf("容器归属项目 %q 与预留项目 %s 不一致: %w", r.Spec.Ownership.Project, r.ProjectID, fs.ErrInvalid)
	}
	return nil
}

// Execution is the result of a successful admission and start. When Start
// returns an error after capacity was reserved, the returned Execution contains
// the reservation and, if a container was created, its identifier so the caller
// can retry Stop when cleanup did not finish.
type Execution struct {
	Container   containers.Container
	Observation containers.Observation
	Reservation capacity.Reservation
}

// Start reserves host and project capacity, then creates and starts the
// container. The reservation is made before any Docker operation, so an
// over-budget request never starts a container. Repeating the same request for
// an active execution adopts its existing container. A definite create failure
// releases the reservation; retrying that execution identifier then returns
// capacity.ErrExecutionReleased, so a retry needs a new identifier.
func (s *Service) Start(ctx context.Context, request StartRequest) (Execution, error) {
	if err := request.validate(); err != nil {
		return Execution{}, err
	}
	unlock := s.lockExecution(request.ExecutionID)
	defer unlock()

	reservation, created, err := s.reservations.Reserve(ctx, capacity.Request{
		ExecutionID: request.ExecutionID,
		HostID:      s.hostID,
		ProjectID:   request.ProjectID,
		Resources: capacity.Resources{
			CPUMillis:   request.Spec.Limits.CPUMillis,
			MemoryBytes: request.Spec.Limits.MemoryBytes,
			Instances:   1,
		},
	})
	if err != nil {
		return Execution{}, fmt.Errorf("准入执行 %s: %w", request.ExecutionID, err)
	}

	if !created {
		existing, found, err := s.containers.Find(ctx, request.Spec.Ownership)
		if err != nil {
			return Execution{Reservation: reservation}, fmt.Errorf("查找已准入执行 %s 的容器: %w", request.ExecutionID, err)
		}
		if found {
			if err := checkAdoptedContainer(request.ExecutionID, existing, request.Spec); err != nil {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, err
			}
			if existing.Running {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, nil
			}
			observation, err := s.containers.Start(ctx, request.Spec.Ownership, existing.ID)
			if err != nil {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, fmt.Errorf("恢复执行 %s 的容器: %w", request.ExecutionID, err)
			}
			return Execution{
				Container:   containers.Container{ID: observation.ID, Name: observation.Name},
				Observation: observation,
				Reservation: reservation,
			}, nil
		}
		// The reservation survived a previous attempt but no container exists.
		// Continue with creation so a retry can finish the execution.
	}

	createdContainer, err := s.containers.Create(ctx, request.Spec)
	if err != nil {
		cause := fmt.Errorf("创建执行 %s 的容器: %w", request.ExecutionID, err)
		// A failed create is normally a definite failure with no container.
		// Check the deterministic name anyway: Docker may have created the
		// container before the response was lost, and a concurrent controller
		// may own the name. Adopt it only when it carries this execution's
		// ownership; otherwise keep the reservation and report the blocker.
		existing, found, findErr := s.containers.Find(ctx, request.Spec.Ownership)
		if findErr != nil {
			return Execution{Reservation: reservation}, errors.Join(cause, fmt.Errorf("检查执行 %s 的容器: %w", request.ExecutionID, findErr))
		}
		if found {
			if err := checkAdoptedContainer(request.ExecutionID, existing, request.Spec); err != nil {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, errors.Join(cause, err)
			}
			if existing.Running {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, nil
			}
			observation, startErr := s.containers.Start(ctx, request.Spec.Ownership, existing.ID)
			if startErr != nil {
				return Execution{
					Container:   containers.Container{ID: existing.ID, Name: existing.Name},
					Observation: existing,
					Reservation: reservation,
				}, errors.Join(cause, fmt.Errorf("恢复执行 %s 的容器: %w", request.ExecutionID, startErr))
			}
			return Execution{
				Container:   containers.Container{ID: observation.ID, Name: observation.Name},
				Observation: observation,
				Reservation: reservation,
			}, nil
		}
		return Execution{}, s.releaseAfterFailure(ctx, request.ProjectID, request.ExecutionID, cause)
	}

	observation, err := s.containers.Start(ctx, request.Spec.Ownership, createdContainer.ID)
	if err != nil {
		cause := fmt.Errorf("启动执行 %s 的容器: %w", request.ExecutionID, err)
		return Execution{
			Container:   createdContainer,
			Reservation: reservation,
		}, s.cleanupAfterStartFailure(ctx, request, createdContainer, cause)
	}
	return Execution{
		Container:   createdContainer,
		Observation: observation,
		Reservation: reservation,
	}, nil
}

// StopRequest asks to stop and remove one execution. ContainerID is the
// identifier returned by Start; requiring it keeps capacity release tied to a
// container whose ownership was verified. Stop releases capacity only after the
// container is confirmed removed, and a repeated Stop is idempotent.
type StopRequest struct {
	ExecutionID uuid.UUID
	ProjectID   uuid.UUID
	Ownership   containers.Ownership
	ContainerID string
}

func (r StopRequest) validate() error {
	if r.ExecutionID == uuid.Nil {
		return fmt.Errorf("停止执行需要执行标识: %w", fs.ErrInvalid)
	}
	if r.ProjectID == uuid.Nil {
		return fmt.Errorf("停止执行需要项目标识: %w", fs.ErrInvalid)
	}
	if err := r.Ownership.Validate(); err != nil {
		return err
	}
	if r.Ownership.Project != r.ProjectID.String() {
		return fmt.Errorf("容器归属项目 %q 与预留项目 %s 不一致: %w", r.Ownership.Project, r.ProjectID, fs.ErrInvalid)
	}
	if r.ContainerID == "" {
		return fmt.Errorf("停止执行需要启动返回的容器标识: %w", fs.ErrInvalid)
	}
	return nil
}

// Stop stops and removes the execution's container, then releases its capacity.
// A missing container is treated as confirmed cleanup. If stopping or removing
// fails, the reservation stays active.
func (s *Service) Stop(ctx context.Context, request StopRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	unlock := s.lockExecution(request.ExecutionID)
	defer unlock()

	reservation, err := s.reservations.GetReservation(ctx, request.ProjectID, request.ExecutionID)
	if err != nil {
		return fmt.Errorf("停止执行 %s: %w", request.ExecutionID, err)
	}
	if reservation.HostID != s.hostID {
		return fmt.Errorf("停止执行 %s: 容量预留属于主机 %s，当前服务主机为 %s: %w", request.ExecutionID, reservation.HostID, s.hostID, fs.ErrInvalid)
	}

	containerID := request.ContainerID
	if err := s.containers.Stop(ctx, request.Ownership, containerID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return fmt.Errorf("停止执行 %s 的容器 %s: %w", request.ExecutionID, containerID, err)
	}
	if err := s.containers.Remove(ctx, request.Ownership, containerID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return fmt.Errorf("删除执行 %s 的容器 %s: %w", request.ExecutionID, containerID, err)
	}
	// Capacity is released only after the container is confirmed gone. Use a
	// detached context so a cancelled request does not leak an already cleaned
	// reservation.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return s.release(releaseCtx, request.ProjectID, request.ExecutionID)
}

func checkAdoptedContainer(executionID uuid.UUID, existing containers.Observation, spec containers.Spec) error {
	if existing.Image != spec.Image {
		return fmt.Errorf("%w: 执行 %s 的容器镜像 %q 与请求 %q 不一致", ErrExecutionConflict, executionID, existing.Image, spec.Image)
	}
	if existing.CPUMillis != spec.Limits.CPUMillis || existing.MemoryBytes != spec.Limits.MemoryBytes {
		return fmt.Errorf("%w: 执行 %s 的容器资源上限为 CPU %d 毫核、内存 %d 字节，请求为 CPU %d 毫核、内存 %d 字节",
			ErrExecutionConflict, executionID, existing.CPUMillis, existing.MemoryBytes, spec.Limits.CPUMillis, spec.Limits.MemoryBytes)
	}
	return nil
}

func (s *Service) releaseAfterFailure(ctx context.Context, projectID, executionID uuid.UUID, cause error) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := s.reservations.Release(releaseCtx, projectID, executionID); err != nil {
		return errors.Join(cause, fmt.Errorf("释放执行 %s 的容量预留: %w", executionID, err))
	}
	return cause
}

func (s *Service) cleanupAfterStartFailure(ctx context.Context, request StartRequest, created containers.Container, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if err := s.containers.Stop(cleanupCtx, request.Spec.Ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("保留执行 %s 的容量预留: 停止容器 %s 失败: %w", request.ExecutionID, created.ID, err))
	}
	if err := s.containers.Remove(cleanupCtx, request.Spec.Ownership, created.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("保留执行 %s 的容量预留: 删除容器 %s 失败: %w", request.ExecutionID, created.ID, err))
	}
	if _, err := s.reservations.Release(cleanupCtx, request.ProjectID, request.ExecutionID); err != nil {
		return errors.Join(cause, fmt.Errorf("清理容器 %s 后释放执行 %s 的容量预留失败: %w", created.ID, request.ExecutionID, err))
	}
	return cause
}

func (s *Service) release(ctx context.Context, projectID, executionID uuid.UUID) error {
	if _, err := s.reservations.Release(ctx, projectID, executionID); err != nil {
		return fmt.Errorf("释放执行 %s 的容量预留: %w", executionID, err)
	}
	return nil
}

func (s *Service) lockExecution(executionID uuid.UUID) func() {
	s.locksMu.Lock()
	lock := s.locks[executionID]
	if lock == nil {
		lock = &executionLock{}
		s.locks[executionID] = lock
	}
	lock.refs++
	s.locksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(s.locks, executionID)
		}
		s.locksMu.Unlock()
	}
}
