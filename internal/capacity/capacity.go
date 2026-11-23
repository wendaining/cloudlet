// Package capacity reserves host and project resources in PostgreSQL.
//
// A reservation is keyed by a stable execution identifier. Reserve checks the
// host budget and the project quota in one transaction, Release keeps a
// terminal record after the caller confirms cleanup, and Usage reports the
// resources currently held by active reservations.
package capacity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	stateActive   = "active"
	stateReleased = "released"

	maxHostIDLength       = 64
	maxBindingFieldLength = 48
	maxImageLength        = 255
)

var (
	// ErrHostNotFound reports that a host budget has not been configured.
	ErrHostNotFound = errors.New("主机容量未配置")
	// ErrProjectNotFound reports that the project does not exist.
	ErrProjectNotFound = errors.New("项目不存在")
	// ErrProjectQuotaNotFound reports that a project quota has not been configured.
	ErrProjectQuotaNotFound = errors.New("项目配额未配置")
	// ErrInsufficientCapacity reports that the host or project budget cannot
	// satisfy the requested resources.
	ErrInsufficientCapacity = errors.New("容量不足")
	// ErrIdempotencyConflict reports that an execution identifier was already
	// reserved with a different host, project, resource or container binding
	// request.
	ErrIdempotencyConflict = errors.New("执行标识已用于不同预留")
	// ErrExecutionReleased reports that an execution identifier has already
	// been released and cannot reserve capacity again.
	ErrExecutionReleased = errors.New("执行标识的容量预留已释放")
	// ErrQuotaInUse reports that a new limit is below current active usage.
	ErrQuotaInUse = errors.New("配额低于当前占用")
	// ErrReservationNotFound reports that the project has no reservation for
	// the requested execution identifier.
	ErrReservationNotFound = errors.New("容量预留不存在")
	// ErrOwnershipConflict reports that another active execution already owns
	// the requested container binding.
	ErrOwnershipConflict = errors.New("容器归属已由其他执行使用")
	// ErrBindingNotFound reports that a reservation has no container binding.
	ErrBindingNotFound = errors.New("容量预留未绑定容器归属")
)

// Resources describes CPU, memory and instance amounts. CPU uses millicores,
// memory uses bytes, and Instances counts execution slots.
type Resources struct {
	CPUMillis   int64
	MemoryBytes int64
	Instances   int64
}

func (r Resources) validate() error {
	if r.CPUMillis <= 0 {
		return fmt.Errorf("容量需要正的 CPU 毫核: %w", fs.ErrInvalid)
	}
	if r.MemoryBytes <= 0 {
		return fmt.Errorf("容量需要正的内存字节: %w", fs.ErrInvalid)
	}
	if r.Instances <= 0 {
		return fmt.Errorf("容量需要正的实例数: %w", fs.ErrInvalid)
	}
	return nil
}

// Binding identifies the managed container that owns a reservation. The tuple
// of environment, project, resource and generation is unique among active
// reservations, so an execution can only retry with the container ownership
// recorded at admission.
type Binding struct {
	Environment string
	Project     string
	Resource    string
	Generation  uint64
	Image       string
}

func (b Binding) isZero() bool {
	return b.Environment == "" && b.Project == "" && b.Resource == "" && b.Generation == 0 && b.Image == ""
}

func (b Binding) validate() error {
	if b.isZero() {
		return nil
	}
	if err := validateBindingIdentifier("容器归属环境", b.Environment); err != nil {
		return err
	}
	if err := validateBindingIdentifier("容器归属项目", b.Project); err != nil {
		return err
	}
	if err := validateBindingIdentifier("容器归属资源", b.Resource); err != nil {
		return err
	}
	if b.Generation == 0 || b.Generation > math.MaxInt64 {
		return fmt.Errorf("容器归属执行序号需要正整数: %w", fs.ErrInvalid)
	}
	if b.Image == "" || len(b.Image) > maxImageLength || !utf8.ValidString(b.Image) ||
		strings.IndexFunc(b.Image, func(char rune) bool { return char == 0 || unicode.IsSpace(char) }) >= 0 {
		return fmt.Errorf("容器镜像需要 1 至 %d 字节且不含空白: %w", maxImageLength, fs.ErrInvalid)
	}
	return nil
}

// HostCapacity is the configured budget for one host or environment.
type HostCapacity struct {
	ID        string
	Capacity  Resources
	UpdatedAt time.Time
}

// ProjectQuota is the configured budget for one project.
type ProjectQuota struct {
	ProjectID uuid.UUID
	Capacity  Resources
	UpdatedAt time.Time
}

// ReservationState is the lifecycle state of a capacity reservation.
type ReservationState string

const (
	// Active reservations hold capacity.
	Active ReservationState = stateActive
	// Released reservations no longer hold capacity.
	Released ReservationState = stateReleased
)

// Reservation records the capacity held by one execution and, when bound, the
// managed container that owns it.
type Reservation struct {
	ExecutionID uuid.UUID
	HostID      string
	ProjectID   uuid.UUID
	Resources   Resources
	Binding     Binding
	ContainerID string
	State       ReservationState
	CreatedAt   time.Time
	ReleasedAt  *time.Time
}

// Request asks to reserve resources for one execution on one host. Binding is
// optional for capacity-only callers; the execution service always supplies it.
type Request struct {
	ExecutionID uuid.UUID
	HostID      string
	ProjectID   uuid.UUID
	Resources   Resources
	Binding     Binding
}

func (r Request) validate() error {
	if r.ExecutionID == uuid.Nil {
		return fmt.Errorf("容量预留需要执行标识: %w", fs.ErrInvalid)
	}
	if r.ProjectID == uuid.Nil {
		return fmt.Errorf("容量预留需要项目标识: %w", fs.ErrInvalid)
	}
	if err := validateHostID(r.HostID); err != nil {
		return err
	}
	if err := r.Resources.validate(); err != nil {
		return err
	}
	return r.Binding.validate()
}

// Usage reports configured limits and active usage for a host and project.
type Usage struct {
	HostID       string
	HostLimit    Resources
	HostUsed     Resources
	ProjectID    uuid.UUID
	ProjectLimit Resources
	ProjectUsed  Resources
}

// Store reserves capacity in PostgreSQL. It is safe for concurrent use.
type Store struct {
	db *gorm.DB
}

// NewStore requires an initialized PostgreSQL connection.
func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("容量预留存储需要 PostgreSQL 连接")
	}
	return &Store{db: db}, nil
}

// writeTransaction uses READ COMMITTED because capacity checks read usage
// after acquiring host and project row locks. Under REPEATABLE READ the
// transaction snapshot is established by an earlier query, before the lock
// wait, so a transaction that acquires the lock after another commit would
// still miss that reservation and could overcommit.
func (s *Store) writeTransaction(ctx context.Context, fn func(*gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

type hostCapacityRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	CPUMillis   int64     `gorm:"column:cpu_millis"`
	MemoryBytes int64     `gorm:"column:memory_bytes"`
	Instances   int64     `gorm:"column:instances"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (hostCapacityRow) TableName() string { return "capacity_hosts" }

func (r hostCapacityRow) resources() Resources {
	return Resources{CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes, Instances: r.Instances}
}

type projectQuotaRow struct {
	ProjectID   uuid.UUID `gorm:"column:project_id;primaryKey"`
	CPUMillis   int64     `gorm:"column:cpu_millis"`
	MemoryBytes int64     `gorm:"column:memory_bytes"`
	Instances   int64     `gorm:"column:instances"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (projectQuotaRow) TableName() string { return "capacity_project_quotas" }

func (r projectQuotaRow) resources() Resources {
	return Resources{CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes, Instances: r.Instances}
}

type reservationRow struct {
	ExecutionID          uuid.UUID  `gorm:"column:execution_id;primaryKey"`
	HostID               string     `gorm:"column:host_id"`
	ProjectID            uuid.UUID  `gorm:"column:project_id"`
	CPUMillis            int64      `gorm:"column:cpu_millis"`
	MemoryBytes          int64      `gorm:"column:memory_bytes"`
	Instances            int64      `gorm:"column:instances"`
	ContainerEnvironment *string    `gorm:"column:container_environment"`
	ContainerProject     *string    `gorm:"column:container_project"`
	ContainerResource    *string    `gorm:"column:container_resource"`
	ContainerGeneration  *int64     `gorm:"column:container_generation"`
	ContainerImage       *string    `gorm:"column:container_image"`
	ContainerID          *string    `gorm:"column:container_id"`
	State                string     `gorm:"column:state"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	ReleasedAt           *time.Time `gorm:"column:released_at"`
}

func (reservationRow) TableName() string { return "capacity_reservations" }

func (r reservationRow) resources() Resources {
	return Resources{CPUMillis: r.CPUMillis, MemoryBytes: r.MemoryBytes, Instances: r.Instances}
}

func (r reservationRow) binding() Binding {
	if r.ContainerEnvironment == nil || r.ContainerProject == nil || r.ContainerResource == nil ||
		r.ContainerGeneration == nil || r.ContainerImage == nil {
		return Binding{}
	}
	return Binding{
		Environment: *r.ContainerEnvironment,
		Project:     *r.ContainerProject,
		Resource:    *r.ContainerResource,
		Generation:  uint64(*r.ContainerGeneration),
		Image:       *r.ContainerImage,
	}
}

func (r reservationRow) containerID() string {
	if r.ContainerID == nil {
		return ""
	}
	return *r.ContainerID
}

func (r reservationRow) toReservation() Reservation {
	return Reservation{
		ExecutionID: r.ExecutionID,
		HostID:      r.HostID,
		ProjectID:   r.ProjectID,
		Resources:   r.resources(),
		Binding:     r.binding(),
		ContainerID: r.containerID(),
		State:       ReservationState(r.State),
		CreatedAt:   r.CreatedAt,
		ReleasedAt:  r.ReleasedAt,
	}
}

type usageTotals struct {
	CPUMillis   int64 `gorm:"column:cpu_millis"`
	MemoryBytes int64 `gorm:"column:memory_bytes"`
	Instances   int64 `gorm:"column:instances"`
}

func (u usageTotals) resources() Resources {
	return Resources{CPUMillis: u.CPUMillis, MemoryBytes: u.MemoryBytes, Instances: u.Instances}
}

// SetHostCapacity creates or updates a host budget. A new limit below current
// active usage returns ErrQuotaInUse.
func (s *Store) SetHostCapacity(ctx context.Context, hostID string, capacity Resources) (HostCapacity, error) {
	if err := validateHostID(hostID); err != nil {
		return HostCapacity{}, err
	}
	if err := capacity.validate(); err != nil {
		return HostCapacity{}, err
	}
	var result HostCapacity
	err := s.writeTransaction(ctx, func(tx *gorm.DB) error {
		now := currentTime()
		row := hostCapacityRow{
			ID:          hostID,
			CPUMillis:   capacity.CPUMillis,
			MemoryBytes: capacity.MemoryBytes,
			Instances:   capacity.Instances,
			UpdatedAt:   now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("登记主机 %s 的容量: %w", hostID, err)
		}
		var stored hostCapacityRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", hostID).Take(&stored).Error; err != nil {
			return fmt.Errorf("锁定主机 %s 的容量: %w", hostID, err)
		}
		used, err := hostUsage(tx, hostID, uuid.Nil)
		if err != nil {
			return err
		}
		if err := ensureCapacityAtLeast("主机", hostID, capacity, used); err != nil {
			return err
		}
		if err := tx.Model(&hostCapacityRow{}).Where("id = ?", hostID).Updates(map[string]any{
			"cpu_millis":   capacity.CPUMillis,
			"memory_bytes": capacity.MemoryBytes,
			"instances":    capacity.Instances,
			"updated_at":   now,
		}).Error; err != nil {
			return fmt.Errorf("更新主机 %s 的容量: %w", hostID, err)
		}
		result = HostCapacity{ID: hostID, Capacity: capacity, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return HostCapacity{}, err
	}
	return result, nil
}

// SetProjectQuota creates or updates a project quota. A new limit below
// current active usage returns ErrQuotaInUse.
func (s *Store) SetProjectQuota(ctx context.Context, projectID uuid.UUID, capacity Resources) (ProjectQuota, error) {
	if projectID == uuid.Nil {
		return ProjectQuota{}, fmt.Errorf("设置项目配额需要项目标识: %w", fs.ErrInvalid)
	}
	if err := capacity.validate(); err != nil {
		return ProjectQuota{}, err
	}
	var result ProjectQuota
	err := s.writeTransaction(ctx, func(tx *gorm.DB) error {
		var projectCount int64
		if err := tx.Raw("SELECT COUNT(*) FROM projects WHERE id = ?", projectID).Scan(&projectCount).Error; err != nil {
			return fmt.Errorf("检查项目 %s: %w", projectID, err)
		}
		if projectCount == 0 {
			return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
		}
		now := currentTime()
		row := projectQuotaRow{
			ProjectID:   projectID,
			CPUMillis:   capacity.CPUMillis,
			MemoryBytes: capacity.MemoryBytes,
			Instances:   capacity.Instances,
			UpdatedAt:   now,
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}},
			DoNothing: true,
		}).Create(&row).Error; err != nil {
			return fmt.Errorf("登记项目 %s 的配额: %w", projectID, err)
		}
		var stored projectQuotaRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ?", projectID).Take(&stored).Error; err != nil {
			return fmt.Errorf("锁定项目 %s 的配额: %w", projectID, err)
		}
		used, err := projectUsage(tx, projectID, uuid.Nil)
		if err != nil {
			return err
		}
		if err := ensureCapacityAtLeast("项目", projectID.String(), capacity, used); err != nil {
			return err
		}
		if err := tx.Model(&projectQuotaRow{}).Where("project_id = ?", projectID).Updates(map[string]any{
			"cpu_millis":   capacity.CPUMillis,
			"memory_bytes": capacity.MemoryBytes,
			"instances":    capacity.Instances,
			"updated_at":   now,
		}).Error; err != nil {
			return fmt.Errorf("更新项目 %s 的配额: %w", projectID, err)
		}
		result = ProjectQuota{ProjectID: projectID, Capacity: capacity, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return ProjectQuota{}, err
	}
	return result, nil
}

// Reserve records capacity for an execution. The returned bool is true only
// when this call created the reservation. Repeating the same request returns
// the existing active reservation. The same execution identifier with a
// different host, project, resource or container binding request returns
// ErrIdempotencyConflict. A container binding owned by another active execution
// returns ErrOwnershipConflict.
func (s *Store) Reserve(ctx context.Context, request Request) (Reservation, bool, error) {
	if err := request.validate(); err != nil {
		return Reservation{}, false, err
	}
	var result Reservation
	var created bool
	err := s.writeTransaction(ctx, func(tx *gorm.DB) error {
		// Resolve an existing execution before checking the requested host so
		// idempotency and conflict results do not depend on current budget
		// configuration. The lock also serializes with a concurrent release.
		var existing reservationRow
		existingErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("execution_id = ?", request.ExecutionID).Take(&existing).Error
		if existingErr == nil {
			var err error
			result, created, err = resolveExisting(tx, &existing, request)
			return err
		}
		if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("读取执行 %s 的已有预留: %w", request.ExecutionID, existingErr)
		}

		// Every capacity mutation locks the host row first, then the project
		// quota row. This serializes reservations that share a host budget and
		// keeps concurrent reservations from observing partial usage.
		var host hostCapacityRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", request.HostID).Take(&host).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrHostNotFound, request.HostID)
			}
			return fmt.Errorf("锁定主机 %s 的容量: %w", request.HostID, err)
		}

		if !request.Binding.isZero() {
			if err := lockBinding(tx, request.Binding); err != nil {
				return err
			}
		}

		// Claim the execution identifier before checking usage. The unique
		// indexes make concurrent duplicates wait for the winner, including
		// requests that use different hosts or container bindings.
		row := reservationRow{
			ExecutionID: request.ExecutionID,
			HostID:      request.HostID,
			ProjectID:   request.ProjectID,
			CPUMillis:   request.Resources.CPUMillis,
			MemoryBytes: request.Resources.MemoryBytes,
			Instances:   request.Resources.Instances,
			State:       stateActive,
			CreatedAt:   currentTime(),
		}
		applyBinding(&row, request.Binding)
		insert := tx.Clauses(
			clause.OnConflict{DoNothing: true},
			clause.Returning{},
		).Create(&row)
		if insert.Error != nil {
			return fmt.Errorf("登记执行 %s 的容量预留: %w", request.ExecutionID, insert.Error)
		}
		if insert.RowsAffected == 0 {
			// The execution identifier or the container binding already
			// exists. Resolve the execution first so an idempotent retry keeps
			// its original result.
			var existing reservationRow
			existingErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("execution_id = ?", request.ExecutionID).Take(&existing).Error
			if existingErr == nil {
				var err error
				result, created, err = resolveExisting(tx, &existing, request)
				return err
			}
			if !errors.Is(existingErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("读取执行 %s 的已有预留: %w", request.ExecutionID, existingErr)
			}
			if !request.Binding.isZero() {
				var owner reservationRow
				ownerErr := tx.Where("state = ? AND container_environment = ? AND container_project = ? AND container_resource = ? AND container_generation = ?",
					stateActive, request.Binding.Environment, request.Binding.Project, request.Binding.Resource,
					int64(request.Binding.Generation)).Take(&owner).Error
				if ownerErr == nil {
					return fmt.Errorf("%w: 容器归属 %s/%s/%s/%d 已由执行 %s 使用",
						ErrOwnershipConflict, request.Binding.Environment, request.Binding.Project,
						request.Binding.Resource, request.Binding.Generation, owner.ExecutionID)
				}
				if !errors.Is(ownerErr, gorm.ErrRecordNotFound) {
					return fmt.Errorf("检查执行 %s 的容器归属: %w", request.ExecutionID, ownerErr)
				}
			}
			return fmt.Errorf("登记执行 %s 的容量预留: 冲突但未找到已有记录", request.ExecutionID)
		}

		var quota projectQuotaRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ?", request.ProjectID).Take(&quota).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrProjectQuotaNotFound, request.ProjectID)
			}
			return fmt.Errorf("锁定项目 %s 的配额: %w", request.ProjectID, err)
		}

		// The claim row belongs to this transaction. Exclude it so the check
		// counts only capacity already committed by other transactions.
		hostUsed, err := hostUsage(tx, request.HostID, request.ExecutionID)
		if err != nil {
			return err
		}
		projectUsed, err := projectUsage(tx, request.ProjectID, request.ExecutionID)
		if err != nil {
			return err
		}
		if err := checkAvailable("主机", request.HostID, host.resources(), hostUsed, request.Resources); err != nil {
			return err
		}
		if err := checkAvailable("项目", request.ProjectID.String(), quota.resources(), projectUsed, request.Resources); err != nil {
			return err
		}
		result = row.toReservation()
		created = true
		return nil
	})
	if err != nil {
		return Reservation{}, false, err
	}
	return result, created, nil
}

func resolveExisting(tx *gorm.DB, existing *reservationRow, request Request) (Reservation, bool, error) {
	if err := validateExisting(existing, request); err != nil {
		return Reservation{}, false, err
	}
	if !request.Binding.isZero() && existing.binding().isZero() {
		if err := bindExisting(tx, existing, request.Binding); err != nil {
			return Reservation{}, false, err
		}
	}
	switch existing.State {
	case stateActive:
		return existing.toReservation(), false, nil
	case stateReleased:
		return Reservation{}, false, fmt.Errorf("%w: 执行标识 %s", ErrExecutionReleased, request.ExecutionID)
	default:
		return Reservation{}, false, fmt.Errorf("执行标识 %s 的预留状态 %q 无效", request.ExecutionID, existing.State)
	}
}

func validateExisting(existing *reservationRow, request Request) error {
	if existing.HostID != request.HostID || existing.ProjectID != request.ProjectID ||
		existing.resources() != request.Resources {
		return fmt.Errorf("%w: 执行标识 %s 已用于其他预留", ErrIdempotencyConflict, request.ExecutionID)
	}
	if request.Binding.isZero() {
		return nil
	}
	current := existing.binding()
	if current.isZero() {
		if existing.State != stateActive {
			return fmt.Errorf("%w: 执行标识 %s", ErrExecutionReleased, request.ExecutionID)
		}
		return nil
	}
	if current != request.Binding {
		return fmt.Errorf("%w: 执行标识 %s 已绑定其他容器归属", ErrIdempotencyConflict, request.ExecutionID)
	}
	return nil
}

func bindExisting(tx *gorm.DB, existing *reservationRow, binding Binding) error {
	if err := lockBinding(tx, binding); err != nil {
		return err
	}
	var owner reservationRow
	ownerErr := tx.Where("state = ? AND container_environment = ? AND container_project = ? AND container_resource = ? AND container_generation = ? AND execution_id <> ?",
		stateActive, binding.Environment, binding.Project, binding.Resource, int64(binding.Generation), existing.ExecutionID).
		Take(&owner).Error
	if ownerErr == nil {
		return fmt.Errorf("%w: 容器归属 %s/%s/%s/%d 已由执行 %s 使用",
			ErrOwnershipConflict, binding.Environment, binding.Project, binding.Resource, binding.Generation, owner.ExecutionID)
	}
	if !errors.Is(ownerErr, gorm.ErrRecordNotFound) {
		return fmt.Errorf("检查执行 %s 的容器归属: %w", existing.ExecutionID, ownerErr)
	}
	update := tx.Model(&reservationRow{}).
		Where("execution_id = ? AND state = ? AND container_environment IS NULL", existing.ExecutionID, stateActive).
		Updates(bindingColumns(binding))
	if update.Error != nil {
		return fmt.Errorf("绑定执行 %s 的容器归属: %w", existing.ExecutionID, update.Error)
	}
	if update.RowsAffected != 1 {
		return fmt.Errorf("绑定执行 %s 的容器归属: 状态已变化", existing.ExecutionID)
	}
	applyBinding(existing, binding)
	return nil
}

func lockBinding(tx *gorm.DB, binding Binding) error {
	key := fmt.Sprintf("%d:%s%d:%s%d:%s%d", len(binding.Environment), binding.Environment,
		len(binding.Project), binding.Project, len(binding.Resource), binding.Resource, binding.Generation)
	if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
		return fmt.Errorf("锁定容器归属: %w", err)
	}
	return nil
}

func applyBinding(row *reservationRow, binding Binding) {
	if binding.isZero() {
		return
	}
	environment := binding.Environment
	project := binding.Project
	resource := binding.Resource
	generation := int64(binding.Generation)
	image := binding.Image
	row.ContainerEnvironment = &environment
	row.ContainerProject = &project
	row.ContainerResource = &resource
	row.ContainerGeneration = &generation
	row.ContainerImage = &image
}

func bindingColumns(binding Binding) map[string]any {
	return map[string]any{
		"container_environment": binding.Environment,
		"container_project":     binding.Project,
		"container_resource":    binding.Resource,
		"container_generation":  int64(binding.Generation),
		"container_image":       binding.Image,
	}
}

// SetContainerID records the managed container that owns an active reservation.
// It is idempotent for the same identifier and allows replacing a stale
// identifier after a container was recreated.
func (s *Store) SetContainerID(ctx context.Context, projectID, executionID uuid.UUID, containerID string) (Reservation, error) {
	if projectID == uuid.Nil || executionID == uuid.Nil {
		return Reservation{}, fmt.Errorf("绑定容器需要项目和执行标识: %w", fs.ErrInvalid)
	}
	if err := validateContainerID(containerID); err != nil {
		return Reservation{}, err
	}
	var result Reservation
	err := s.writeTransaction(ctx, func(tx *gorm.DB) error {
		var current reservationRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("execution_id = ?", executionID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrReservationNotFound, executionID)
			}
			return fmt.Errorf("读取执行 %s 的容量预留: %w", executionID, err)
		}
		if current.ProjectID != projectID {
			return fmt.Errorf("%w: %s", ErrReservationNotFound, executionID)
		}
		if current.State != stateActive {
			return fmt.Errorf("%w: 执行标识 %s", ErrExecutionReleased, executionID)
		}
		if current.binding().isZero() {
			return fmt.Errorf("%w: %s", ErrBindingNotFound, executionID)
		}
		if current.containerID() != containerID {
			update := tx.Model(&reservationRow{}).
				Where("execution_id = ? AND state = ?", executionID, stateActive).
				Updates(map[string]any{"container_id": containerID})
			if update.Error != nil {
				return fmt.Errorf("记录执行 %s 的容器标识: %w", executionID, update.Error)
			}
			if update.RowsAffected != 1 {
				return fmt.Errorf("记录执行 %s 的容器标识: 状态已变化", executionID)
			}
			current.ContainerID = &containerID
		}
		result = current.toReservation()
		return nil
	})
	if err != nil {
		return Reservation{}, err
	}
	return result, nil
}

// Release marks a reservation as released after the caller has confirmed that
// the execution and its side effects are gone. Repeating Release for an
// already released reservation returns the released record. A released
// execution identifier cannot reserve capacity again.
func (s *Store) Release(ctx context.Context, projectID, executionID uuid.UUID) (Reservation, error) {
	if projectID == uuid.Nil || executionID == uuid.Nil {
		return Reservation{}, fmt.Errorf("释放容量需要项目和执行标识: %w", fs.ErrInvalid)
	}
	var result Reservation
	err := s.writeTransaction(ctx, func(tx *gorm.DB) error {
		var current reservationRow
		if err := tx.Where("execution_id = ?", executionID).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrReservationNotFound, executionID)
			}
			return fmt.Errorf("读取执行 %s 的容量预留: %w", executionID, err)
		}
		if current.ProjectID != projectID {
			return fmt.Errorf("%w: %s", ErrReservationNotFound, executionID)
		}
		if current.State == stateReleased {
			result = current.toReservation()
			return nil
		}

		var host hostCapacityRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", current.HostID).Take(&host).Error; err != nil {
			return fmt.Errorf("锁定主机 %s 的容量: %w", current.HostID, err)
		}
		var quota projectQuotaRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ?", projectID).Take(&quota).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("锁定项目 %s 的配额: %w", projectID, err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("execution_id = ?", executionID).Take(&current).Error; err != nil {
			return fmt.Errorf("锁定执行 %s 的容量预留: %w", executionID, err)
		}
		if current.State == stateReleased {
			result = current.toReservation()
			return nil
		}

		now := currentTime()
		update := tx.Model(&reservationRow{}).
			Where("execution_id = ? AND state = ?", executionID, stateActive).
			Updates(map[string]any{"state": stateReleased, "released_at": now})
		if update.Error != nil {
			return fmt.Errorf("释放执行 %s 的容量预留: %w", executionID, update.Error)
		}
		if update.RowsAffected != 1 {
			return fmt.Errorf("释放执行 %s 的容量预留: 状态已变化", executionID)
		}
		current.State = stateReleased
		current.ReleasedAt = &now
		result = current.toReservation()
		return nil
	})
	if err != nil {
		return Reservation{}, err
	}
	return result, nil
}

// GetReservation returns the reservation for a project and execution,
// including released reservations.
func (s *Store) GetReservation(ctx context.Context, projectID, executionID uuid.UUID) (Reservation, error) {
	if projectID == uuid.Nil || executionID == uuid.Nil {
		return Reservation{}, fmt.Errorf("查询容量预留需要项目和执行标识: %w", fs.ErrInvalid)
	}
	var row reservationRow
	if err := s.db.WithContext(ctx).
		Where("project_id = ? AND execution_id = ?", projectID, executionID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Reservation{}, fmt.Errorf("%w: %s", ErrReservationNotFound, executionID)
		}
		return Reservation{}, fmt.Errorf("读取执行 %s 的容量预留: %w", executionID, err)
	}
	return row.toReservation(), nil
}

// Usage returns the host and project limits with the resources held by active
// reservations. Both values are read from one repeatable-read snapshot.
func (s *Store) Usage(ctx context.Context, hostID string, projectID uuid.UUID) (Usage, error) {
	if err := validateHostID(hostID); err != nil {
		return Usage{}, err
	}
	if projectID == uuid.Nil {
		return Usage{}, fmt.Errorf("查询容量占用需要项目标识: %w", fs.ErrInvalid)
	}
	var result Usage
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var host hostCapacityRow
		if err := tx.Where("id = ?", hostID).Take(&host).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrHostNotFound, hostID)
			}
			return fmt.Errorf("读取主机 %s 的容量: %w", hostID, err)
		}
		var quota projectQuotaRow
		if err := tx.Where("project_id = ?", projectID).Take(&quota).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: %s", ErrProjectQuotaNotFound, projectID)
			}
			return fmt.Errorf("读取项目 %s 的配额: %w", projectID, err)
		}
		hostUsed, err := hostUsage(tx, hostID, uuid.Nil)
		if err != nil {
			return err
		}
		projectUsed, err := projectUsage(tx, projectID, uuid.Nil)
		if err != nil {
			return err
		}
		result = Usage{
			HostID:       hostID,
			HostLimit:    host.resources(),
			HostUsed:     hostUsed,
			ProjectID:    projectID,
			ProjectLimit: quota.resources(),
			ProjectUsed:  projectUsed,
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Usage{}, err
	}
	return result, nil
}

func hostUsage(tx *gorm.DB, hostID string, exclude uuid.UUID) (Resources, error) {
	query := `SELECT COALESCE(SUM(cpu_millis), 0)::bigint AS cpu_millis,
		COALESCE(SUM(memory_bytes), 0)::bigint AS memory_bytes,
		COALESCE(SUM(instances), 0)::bigint AS instances
		FROM capacity_reservations WHERE state = ? AND host_id = ?`
	args := []any{stateActive, hostID}
	if exclude != uuid.Nil {
		query += " AND execution_id <> ?"
		args = append(args, exclude)
	}
	var totals usageTotals
	if err := tx.Raw(query, args...).Scan(&totals).Error; err != nil {
		return Resources{}, fmt.Errorf("汇总主机 %s 的容量占用: %w", hostID, err)
	}
	return totals.resources(), nil
}

func projectUsage(tx *gorm.DB, projectID uuid.UUID, exclude uuid.UUID) (Resources, error) {
	query := `SELECT COALESCE(SUM(cpu_millis), 0)::bigint AS cpu_millis,
		COALESCE(SUM(memory_bytes), 0)::bigint AS memory_bytes,
		COALESCE(SUM(instances), 0)::bigint AS instances
		FROM capacity_reservations WHERE state = ? AND project_id = ?`
	args := []any{stateActive, projectID}
	if exclude != uuid.Nil {
		query += " AND execution_id <> ?"
		args = append(args, exclude)
	}
	var totals usageTotals
	if err := tx.Raw(query, args...).Scan(&totals).Error; err != nil {
		return Resources{}, fmt.Errorf("汇总项目 %s 的容量占用: %w", projectID, err)
	}
	return totals.resources(), nil
}

func checkAvailable(scope, id string, limit, used, requested Resources) error {
	if requested.CPUMillis > available(limit.CPUMillis, used.CPUMillis) {
		return insufficient(scope, id, "CPU 毫核", requested.CPUMillis, available(limit.CPUMillis, used.CPUMillis))
	}
	if requested.MemoryBytes > available(limit.MemoryBytes, used.MemoryBytes) {
		return insufficient(scope, id, "内存字节", requested.MemoryBytes, available(limit.MemoryBytes, used.MemoryBytes))
	}
	if requested.Instances > available(limit.Instances, used.Instances) {
		return insufficient(scope, id, "实例", requested.Instances, available(limit.Instances, used.Instances))
	}
	return nil
}

func insufficient(scope, id, resource string, requested, remaining int64) error {
	return fmt.Errorf("%w: %s %s %s 需要 %d，可用 %d", ErrInsufficientCapacity, scope, id, resource, requested, remaining)
}

func available(limit, used int64) int64 {
	if used >= limit {
		return 0
	}
	return limit - used
}

func ensureCapacityAtLeast(scope, id string, limit, used Resources) error {
	if used.CPUMillis <= limit.CPUMillis && used.MemoryBytes <= limit.MemoryBytes && used.Instances <= limit.Instances {
		return nil
	}
	return fmt.Errorf("%w: %s %s 当前占用 CPU %d 毫核、内存 %d 字节、实例 %d，不能降低到 CPU %d 毫核、内存 %d 字节、实例 %d",
		ErrQuotaInUse, scope, id, used.CPUMillis, used.MemoryBytes, used.Instances,
		limit.CPUMillis, limit.MemoryBytes, limit.Instances)
}

func currentTime() time.Time {
	return time.Now().UTC().Truncate(time.Microsecond)
}

// ValidateHostID reports whether hostID can identify a configured host budget.
func ValidateHostID(hostID string) error {
	return validateHostID(hostID)
}

func validateContainerID(containerID string) error {
	if containerID == "" || containerID != strings.TrimSpace(containerID) || len(containerID) > 128 || !utf8.ValidString(containerID) {
		return fmt.Errorf("容器标识需要 1 至 128 字节的有效文本: %w", fs.ErrInvalid)
	}
	if strings.IndexFunc(containerID, func(char rune) bool { return char == 0 || unicode.IsControl(char) }) >= 0 {
		return fmt.Errorf("容器标识不能包含控制字符: %w", fs.ErrInvalid)
	}
	return nil
}

func validateBindingIdentifier(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maxBindingFieldLength || !utf8.ValidString(value) {
		return fmt.Errorf("%s 需要 1 至 %d 字节的有效文本: %w", field, maxBindingFieldLength, fs.ErrInvalid)
	}
	if strings.IndexFunc(value, func(char rune) bool { return char == 0 || unicode.IsControl(char) }) >= 0 {
		return fmt.Errorf("%s 不能包含控制字符: %w", field, fs.ErrInvalid)
	}
	return nil
}

func validateHostID(hostID string) error {
	if hostID == "" || hostID != strings.TrimSpace(hostID) || len(hostID) > maxHostIDLength || !utf8.ValidString(hostID) {
		return fmt.Errorf("主机标识需要 1 至 %d 字节的有效文本: %w", maxHostIDLength, fs.ErrInvalid)
	}
	if strings.IndexFunc(hostID, func(char rune) bool { return char == 0 || unicode.IsControl(char) }) >= 0 {
		return fmt.Errorf("主机标识不能包含控制字符: %w", fs.ErrInvalid)
	}
	return nil
}
