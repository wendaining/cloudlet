// Package executions starts and stops managed containers under transactional
// host and project capacity reservations.
//
// A reservation records the container ownership and the Docker container
// identifier for its execution. Start reserves capacity before any Docker
// operation, then binds the reservation to the deterministic container
// ownership. Retrying the same execution can only restore that ownership; a
// different ownership is rejected before Docker is touched. Stop verifies the
// recorded ownership and container identifier before releasing capacity, and a
// missing identifier alone never proves cleanup.
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

// ErrExecutionConflict reports that an execution's recorded container binding
// does not match the requested ownership, image, resource limits or container
// identifier.
var ErrExecutionConflict = errors.New("执行已有不同的容器配置")

// Service admits and runs containers for one host budget. It is safe for
// concurrent use.
type Service struct {
	reservations *capacity.Store
	containers   *containers.Adapter
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
func NewService(reservations *capacity.Store, containerRuntime *containers.Adapter, hostID string) (*Service, error) {
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
// execution identifier is rejected by the persisted binding.
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
// can retry Stop when cleanup did not finish. Started reports that an existing
// stopped container was started during adoption, so the caller can wait for
// readiness before restoring its route.
type Execution struct {
	Container   containers.Container
	Observation containers.Observation
	Reservation capacity.Reservation
	Started     bool
}

// Start reserves host and project capacity, binds the reservation to the
// requested container ownership, then creates and starts the container. The
// reservation is made before any Docker operation, so an over-budget request
// never starts a container. Repeating the same request for an active execution
// adopts its recorded container. A definite create failure releases the
// reservation; retrying that execution identifier then returns
// capacity.ErrExecutionReleased, so a retry needs a new identifier.
func (s *Service) Start(ctx context.Context, request StartRequest) (Execution, error) {
	if err := request.validate(); err != nil {
		return Execution{}, err
	}
	unlock := s.lockExecution(request.ExecutionID)
	defer unlock()

	binding := bindingFor(request.Spec)
	reservation, _, err := s.reservations.Reserve(ctx, capacity.Request{
		ExecutionID: request.ExecutionID,
		HostID:      s.hostID,
		ProjectID:   request.ProjectID,
		Resources: capacity.Resources{
			CPUMillis:   request.Spec.Limits.CPUMillis,
			MemoryBytes: request.Spec.Limits.MemoryBytes,
			Instances:   1,
		},
		Binding: binding,
	})
	if err != nil {
		return Execution{}, fmt.Errorf("准入执行 %s: %w", request.ExecutionID, err)
	}
	if reservation.Binding != binding {
		return Execution{Reservation: reservation}, fmt.Errorf("%w: 执行 %s 的容器归属与预留不一致", ErrExecutionConflict, request.ExecutionID)
	}

	existing, found, err := s.containers.Find(ctx, request.Spec.Ownership)
	if err != nil {
		return Execution{Reservation: reservation}, fmt.Errorf("查找执行 %s 的容器: %w", request.ExecutionID, err)
	}
	if found {
		return s.adoptExisting(ctx, request, reservation, existing)
	}

	createdContainer, err := s.containers.Create(ctx, request.Spec)
	if err != nil {
		cause := fmt.Errorf("创建执行 %s 的容器: %w", request.ExecutionID, err)
		// A failed create is normally a definite failure with no container.
		// Check the deterministic name anyway: Docker may have created the
		// container before the response was lost, or a concurrent controller
		// may own the name.
		existing, found, findErr := s.containers.Find(ctx, request.Spec.Ownership)
		if findErr != nil {
			return Execution{Reservation: reservation}, errors.Join(cause, fmt.Errorf("检查执行 %s 的容器: %w", request.ExecutionID, findErr))
		}
		if found {
			if len(request.Spec.Files) > 0 {
				return Execution{Container: containers.Container{ID: existing.ID, Name: existing.Name}, Reservation: reservation}, cause
			}
			return s.adoptExisting(ctx, request, reservation, existing)
		}
		released, releaseErr := s.releaseAfterFailure(ctx, request.ProjectID, request.ExecutionID, cause)
		if released {
			return Execution{}, releaseErr
		}
		return Execution{Reservation: reservation}, releaseErr
	}

	updated, err := s.reservations.SetContainerID(ctx, request.ProjectID, request.ExecutionID, createdContainer.ID)
	if err != nil {
		cause := fmt.Errorf("记录执行 %s 的容器标识: %w", request.ExecutionID, err)
		return Execution{
			Container:   createdContainer,
			Reservation: reservation,
		}, s.cleanupAfterStartFailure(ctx, request, createdContainer, cause)
	}
	reservation = updated

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

// Adopt re-binds an existing managed container to its recorded execution
// without creating a container. It reserves capacity idempotently, verifies the
// container ownership and prepared configuration, and starts the container when
// it exists but is stopped. found is false when no container carries the
// ownership, which lets a controller report drift instead of creating a
// duplicate execution. Adoption never releases the reservation on failure.
func (s *Service) Adopt(ctx context.Context, request StartRequest) (execution Execution, found bool, err error) {
	if err := request.validate(); err != nil {
		return Execution{}, false, err
	}
	unlock := s.lockExecution(request.ExecutionID)
	defer unlock()

	binding := bindingFor(request.Spec)
	reservation, _, err := s.reservations.Reserve(ctx, capacity.Request{
		ExecutionID: request.ExecutionID,
		HostID:      s.hostID,
		ProjectID:   request.ProjectID,
		Resources: capacity.Resources{
			CPUMillis:   request.Spec.Limits.CPUMillis,
			MemoryBytes: request.Spec.Limits.MemoryBytes,
			Instances:   1,
		},
		Binding: binding,
	})
	if err != nil {
		return Execution{}, false, fmt.Errorf("准入执行 %s: %w", request.ExecutionID, err)
	}
	if reservation.Binding != binding {
		return Execution{Reservation: reservation}, false, fmt.Errorf("%w: 执行 %s 的容器归属与预留不一致", ErrExecutionConflict, request.ExecutionID)
	}
	existing, found, err := s.containers.Find(ctx, request.Spec.Ownership)
	if err != nil {
		return Execution{Reservation: reservation}, false, fmt.Errorf("查找执行 %s 的容器: %w", request.ExecutionID, err)
	}
	if !found {
		return Execution{Reservation: reservation}, false, nil
	}
	adopted, err := s.adoptExisting(ctx, request, reservation, existing)
	return adopted, true, err
}

func (s *Service) adoptExisting(ctx context.Context, request StartRequest, reservation capacity.Reservation, existing containers.Observation) (Execution, error) {
	if err := checkAdoptedContainer(request.ExecutionID, existing, request.Spec); err != nil {
		return Execution{
			Container:   containers.Container{ID: existing.ID, Name: existing.Name},
			Observation: existing,
			Reservation: reservation,
		}, err
	}
	if reservation.ContainerID != existing.ID {
		updated, err := s.reservations.SetContainerID(ctx, request.ProjectID, request.ExecutionID, existing.ID)
		if err != nil {
			return Execution{
				Container:   containers.Container{ID: existing.ID, Name: existing.Name},
				Observation: existing,
				Reservation: reservation,
			}, fmt.Errorf("记录执行 %s 的容器标识: %w", request.ExecutionID, err)
		}
		reservation = updated
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
			Started:     true,
		}, fmt.Errorf("恢复执行 %s 的容器: %w", request.ExecutionID, err)
	}
	return Execution{
		Container:   containers.Container{ID: observation.ID, Name: observation.Name},
		Observation: observation,
		Reservation: reservation,
		Started:     true,
	}, nil
}

// StopRequest asks to stop and remove one execution. ContainerID is the
// identifier returned by Start. It may be empty only when the reservation has
// no recorded container identifier; when one is recorded, the request must
// match it. Stop releases capacity only after the bound container is confirmed
// removed, and a repeated Stop is idempotent.
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
	return nil
}

// Stop stops and removes the execution's bound container, then releases its
// capacity. The recorded ownership and container identifier are verified before
// any Docker operation. If the recorded container is already gone, the service
// looks up the bound ownership before treating cleanup as confirmed; a missing
// identifier alone never releases capacity. If stopping or removing fails, the
// reservation stays active.
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
	if reservation.State == capacity.Released {
		// Cleanup was already confirmed before release. A container with the
		// same ownership now belongs to a newer execution and must not be
		// touched by a repeated stop of the old execution.
		return nil
	}
	if reservation.Binding == (capacity.Binding{}) {
		return fmt.Errorf("%w: 执行 %s", capacity.ErrBindingNotFound, request.ExecutionID)
	}
	if !bindingMatchesOwnership(reservation.Binding, request.Ownership) {
		return fmt.Errorf("%w: 执行 %s 的容器归属与预留不一致", ErrExecutionConflict, request.ExecutionID)
	}
	if reservation.ContainerID != "" && reservation.ContainerID != request.ContainerID {
		return fmt.Errorf("%w: 执行 %s 的容器标识与预留不一致", ErrExecutionConflict, request.ExecutionID)
	}

	containerID := reservation.ContainerID
	if containerID == "" {
		observation, found, err := s.containers.Find(ctx, request.Ownership)
		if err != nil {
			return fmt.Errorf("查找执行 %s 的容器: %w", request.ExecutionID, err)
		}
		if !found {
			return s.releaseAfterCleanup(ctx, request.ProjectID, request.ExecutionID)
		}
		if err := checkBoundContainer(reservation, observation); err != nil {
			return err
		}
		// Keep the original binding identifier. If release fails later, the
		// caller can retry the same Stop request without matching a newly
		// recorded identifier.
		containerID = observation.ID
	}

	if err := s.containers.Stop(ctx, request.Ownership, containerID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return fmt.Errorf("停止执行 %s 的容器 %s: %w", request.ExecutionID, containerID, err)
	}
	if err := s.containers.Remove(ctx, request.Ownership, containerID); err != nil && !errors.Is(err, containers.ErrNotFound) {
		return fmt.Errorf("删除执行 %s 的容器 %s: %w", request.ExecutionID, containerID, err)
	}

	// The recorded identifier may be stale after a container was recreated or
	// removed outside the service. Confirm by ownership before releasing.
	observation, found, err := s.containers.Find(ctx, request.Ownership)
	if err != nil {
		return fmt.Errorf("确认执行 %s 的容器已删除: %w", request.ExecutionID, err)
	}
	if found {
		if err := checkBoundContainer(reservation, observation); err != nil {
			return err
		}
		if err := s.containers.Stop(ctx, request.Ownership, observation.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			return fmt.Errorf("停止执行 %s 的容器 %s: %w", request.ExecutionID, observation.ID, err)
		}
		if err := s.containers.Remove(ctx, request.Ownership, observation.ID); err != nil && !errors.Is(err, containers.ErrNotFound) {
			return fmt.Errorf("删除执行 %s 的容器 %s: %w", request.ExecutionID, observation.ID, err)
		}
		if _, stillFound, err := s.containers.Find(ctx, request.Ownership); err != nil {
			return fmt.Errorf("确认执行 %s 的容器已删除: %w", request.ExecutionID, err)
		} else if stillFound {
			return fmt.Errorf("执行 %s 的容器仍然存在: %w", request.ExecutionID, fs.ErrInvalid)
		}
	}
	return s.releaseAfterCleanup(ctx, request.ProjectID, request.ExecutionID)
}

func bindingFor(spec containers.Spec) capacity.Binding {
	return capacity.Binding{
		Environment: spec.Ownership.Environment,
		Project:     spec.Ownership.Project,
		Resource:    spec.Ownership.Resource,
		Generation:  spec.Ownership.Generation,
		Image:       spec.Image,
	}
}

func bindingMatchesOwnership(binding capacity.Binding, ownership containers.Ownership) bool {
	return binding.Environment == ownership.Environment &&
		binding.Project == ownership.Project &&
		binding.Resource == ownership.Resource &&
		binding.Generation == ownership.Generation
}

func checkBoundContainer(reservation capacity.Reservation, existing containers.Observation) error {
	if existing.Image != reservation.Binding.Image {
		return fmt.Errorf("%w: 执行 %s 的绑定容器镜像 %q 与预留 %q 不一致", ErrExecutionConflict, reservation.ExecutionID, existing.Image, reservation.Binding.Image)
	}
	if existing.CPUMillis != reservation.Resources.CPUMillis || existing.MemoryBytes != reservation.Resources.MemoryBytes {
		return fmt.Errorf("%w: 执行 %s 的绑定容器资源上限为 CPU %d 毫核、内存 %d 字节，预留为 CPU %d 毫核、内存 %d 字节",
			ErrExecutionConflict, reservation.ExecutionID, existing.CPUMillis, existing.MemoryBytes,
			reservation.Resources.CPUMillis, reservation.Resources.MemoryBytes)
	}
	return nil
}

func checkAdoptedContainer(executionID uuid.UUID, existing containers.Observation, spec containers.Spec) error {
	digest, err := spec.FilesDigest()
	if err != nil {
		return err
	}
	if digest != existing.FilesDigest {
		return fmt.Errorf("%w: execution %s has different prepared files", ErrExecutionConflict, executionID)
	}
	if spec.DataVolume != nil && existing.WritableVolumes[spec.DataVolume.MountPath] != spec.DataVolume.Name {
		return fmt.Errorf("%w: execution %s has a different writable volume at %s", ErrExecutionConflict, executionID, spec.DataVolume.MountPath)
	}
	if existing.Image != spec.Image {
		return fmt.Errorf("%w: 执行 %s 的容器镜像 %q 与请求 %q 不一致", ErrExecutionConflict, executionID, existing.Image, spec.Image)
	}
	if existing.CPUMillis != spec.Limits.CPUMillis || existing.MemoryBytes != spec.Limits.MemoryBytes {
		return fmt.Errorf("%w: 执行 %s 的容器资源上限为 CPU %d 毫核、内存 %d 字节，请求为 CPU %d 毫核、内存 %d 字节",
			ErrExecutionConflict, executionID, existing.CPUMillis, existing.MemoryBytes, spec.Limits.CPUMillis, spec.Limits.MemoryBytes)
	}
	if existing.PIDs != spec.Limits.PIDs || existing.OpenFiles != spec.Limits.OpenFiles {
		return fmt.Errorf("%w: 执行 %s 的容器限制为进程 %d、打开文件 %d，请求为进程 %d、打开文件 %d",
			ErrExecutionConflict, executionID, existing.PIDs, existing.OpenFiles, spec.Limits.PIDs, spec.Limits.OpenFiles)
	}
	return nil
}

func (s *Service) releaseAfterFailure(ctx context.Context, projectID, executionID uuid.UUID, cause error) (bool, error) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := s.reservations.Release(releaseCtx, projectID, executionID); err != nil {
		return false, errors.Join(cause, fmt.Errorf("释放执行 %s 的容量预留: %w", executionID, err))
	}
	return true, cause
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
	observation, found, err := s.containers.Find(cleanupCtx, request.Spec.Ownership)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("保留执行 %s 的容量预留: 确认容器清理失败: %w", request.ExecutionID, err))
	}
	if found {
		return errors.Join(cause, fmt.Errorf("保留执行 %s 的容量预留: 容器 %s 仍然存在", request.ExecutionID, observation.ID))
	}
	if _, err := s.reservations.Release(cleanupCtx, request.ProjectID, request.ExecutionID); err != nil {
		return errors.Join(cause, fmt.Errorf("清理容器 %s 后释放执行 %s 的容量预留失败: %w", created.ID, request.ExecutionID, err))
	}
	return cause
}

func (s *Service) releaseAfterCleanup(ctx context.Context, projectID, executionID uuid.UUID) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := s.reservations.Release(releaseCtx, projectID, executionID); err != nil {
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
