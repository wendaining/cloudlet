package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrContainerAlreadyActive      = errors.New("容器已有活动执行")
	ErrContainerPublicationPending = errors.New("容器已有未完成创建")
	ErrContainerVolumeConflict     = errors.New("容器数据卷声明与已有配置不一致")
)

// Container is the stable identity of one project container. Its active
// execution owns the current configuration, Docker container and public route.
type Container struct {
	ID                uuid.UUID  `gorm:"column:id"`
	ProjectID         uuid.UUID  `gorm:"column:project_id"`
	Name              string     `gorm:"column:name"`
	ActiveExecutionID *uuid.UUID `gorm:"column:active_execution_id"`
	State             string     `gorm:"column:state"`
	Failure           string     `gorm:"column:failure"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
}

func (Container) TableName() string { return "containers" }

// ContainerExecution is one immutable container configuration and its actual
// Docker execution. Command and Env are stored as JSON text.
type ContainerExecution struct {
	ID                uuid.UUID `gorm:"column:id"`
	ProjectID         uuid.UUID `gorm:"column:project_id"`
	ContainerID       uuid.UUID `gorm:"column:container_id"`
	OperationID       uuid.UUID `gorm:"column:operation_id"`
	Generation        uint64    `gorm:"column:generation"`
	Image             string    `gorm:"column:image"`
	ImageDigest       string    `gorm:"column:image_digest"`
	Port              uint16    `gorm:"column:port"`
	Hostname          string    `gorm:"column:hostname"`
	PathPrefix        string    `gorm:"column:path_prefix"`
	CPUMillis         int64     `gorm:"column:cpu_millis"`
	MemoryBytes       int64     `gorm:"column:memory_bytes"`
	PIDs              int64     `gorm:"column:pids"`
	OpenFiles         int64     `gorm:"column:open_files"`
	UserName          string    `gorm:"column:user_name"`
	Command           []string  `gorm:"column:command;type:text;serializer:json"`
	Env               []string  `gorm:"column:env;type:text;serializer:json"`
	HealthPath        string    `gorm:"column:health_path"`
	HealthTimeoutMS   int64     `gorm:"column:health_timeout_ms"`
	VolumeName        string    `gorm:"column:volume_name"`
	VolumeMountPath   string    `gorm:"column:volume_mount_path"`
	VolumeDirectory   string    `gorm:"column:volume_directory"`
	DockerContainerID string    `gorm:"column:docker_container_id"`
	Target            string    `gorm:"column:target"`
	State             string    `gorm:"column:state"`
	Failure           string    `gorm:"column:failure"`
	CreatedAt         time.Time `gorm:"column:created_at"`
}

func (ContainerExecution) TableName() string { return "container_executions" }

// CheckContainerPublication validates existing state before pulling an image.
// CreateContainerExecution repeats the checks while holding the container row
// lock. A failed execution does not block a new attempt with the same name.
func (s *Store) CheckContainerPublication(ctx context.Context, projectID uuid.UUID, name string) error {
	var container Container
	err := s.DB.WithContext(ctx).Where("project_id = ? AND name = ?", projectID, name).Take(&container).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect container %s/%s: %w", projectID, name, err)
	}
	if container.ActiveExecutionID != nil || container.State == "active" {
		return ErrContainerAlreadyActive
	}
	var pending bool
	if err := s.DB.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM container_executions WHERE container_id = ? AND state = 'pending')", container.ID).Row().Scan(&pending); err != nil {
		return fmt.Errorf("inspect pending container execution %s: %w", container.ID, err)
	}
	if pending {
		return ErrContainerPublicationPending
	}
	return nil
}

// CreateContainerExecution records the immutable configuration before any
// Docker operation. It returns the stable container identity and the new
// execution with the next generation.
func (s *Store) CreateContainerExecution(ctx context.Context, name string, execution ContainerExecution) (Container, ContainerExecution, error) {
	if execution.Command == nil {
		execution.Command = []string{}
	}
	if execution.Env == nil {
		execution.Env = []string{}
	}
	candidate := Container{ID: uuid.New(), ProjectID: execution.ProjectID, Name: name, State: "pending"}
	var container Container
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "project_id"}, {Name: "name"}},
			DoNothing: true,
		}).Create(&candidate).Error; err != nil {
			return fmt.Errorf("create container %s/%s: %w", execution.ProjectID, name, err)
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND name = ?", execution.ProjectID, name).Take(&container).Error; err != nil {
			return fmt.Errorf("lock container %s/%s: %w", execution.ProjectID, name, err)
		}
		if container.ActiveExecutionID != nil || container.State == "active" {
			return ErrContainerAlreadyActive
		}
		var pending bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM container_executions WHERE container_id = ? AND state = 'pending')", container.ID).Row().Scan(&pending); err != nil {
			return fmt.Errorf("inspect pending container execution %s: %w", container.ID, err)
		}
		if pending {
			return ErrContainerPublicationPending
		}
		if err := tx.Raw("SELECT COALESCE(MAX(generation), 0) + 1 FROM container_executions WHERE container_id = ?", container.ID).Row().Scan(&execution.Generation); err != nil {
			return fmt.Errorf("allocate container execution generation: %w", err)
		}
		execution.ContainerID = container.ID
		execution.State = "pending"
		execution.Failure = ""
		if err := tx.Create(&execution).Error; err != nil {
			return fmt.Errorf("record container execution %s: %w", execution.ID, err)
		}
		if err := tx.Model(&Container{}).Where("id = ?", container.ID).
			Updates(map[string]any{"state": "pending", "failure": ""}).Error; err != nil {
			return fmt.Errorf("mark container %s pending: %w", container.ID, err)
		}
		container.State = "pending"
		container.Failure = ""
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return container, execution, err
}

func (s *Store) ContainerByOperation(ctx context.Context, projectID, operationID uuid.UUID) (Container, ContainerExecution, error) {
	var execution ContainerExecution
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND operation_id = ?", projectID, operationID).Take(&execution).Error; err != nil {
		return Container{}, ContainerExecution{}, err
	}
	var container Container
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, execution.ContainerID).Take(&container).Error; err != nil {
		return Container{}, ContainerExecution{}, err
	}
	return container, execution, nil
}

// ContainerResult reads one container with its active execution, or the latest
// execution when no execution is active after a failure.
func (s *Store) ContainerResult(ctx context.Context, projectID, containerID uuid.UUID) (Container, ContainerExecution, error) {
	var container Container
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, containerID).Take(&container).Error; err != nil {
		return Container{}, ContainerExecution{}, err
	}
	return s.containerExecution(ctx, container)
}

// ContainerByName reads one container and its active or latest execution by
// project and stable name. It is used to compare a repeated creation request
// with the existing volume declaration before writing a new execution.
func (s *Store) ContainerByName(ctx context.Context, projectID uuid.UUID, name string) (Container, ContainerExecution, error) {
	var container Container
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND name = ?", projectID, name).Take(&container).Error; err != nil {
		return Container{}, ContainerExecution{}, err
	}
	return s.containerExecution(ctx, container)
}

func (s *Store) containerExecution(ctx context.Context, container Container) (Container, ContainerExecution, error) {
	var execution ContainerExecution
	query := s.DB.WithContext(ctx).Where("project_id = ? AND container_id = ?", container.ProjectID, container.ID)
	if container.ActiveExecutionID != nil {
		query = query.Where("id = ?", *container.ActiveExecutionID)
	} else {
		query = query.Order("generation DESC")
	}
	if err := query.Take(&execution).Error; err != nil {
		return Container{}, ContainerExecution{}, err
	}
	return container, execution, nil
}

func (s *Store) SetContainerExecutionDockerID(ctx context.Context, projectID, executionID uuid.UUID, dockerContainerID string) error {
	result := s.DB.WithContext(ctx).Model(&ContainerExecution{}).
		Where("project_id = ? AND id = ? AND state = 'pending'", projectID, executionID).
		Update("docker_container_id", dockerContainerID)
	if result.Error != nil {
		return fmt.Errorf("record container execution %s Docker container: %w", executionID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("container execution %s is not pending", executionID)
	}
	return nil
}

// ActivateContainer atomically marks the execution running, makes it the active
// execution and registers its root route. Existing project routes remain intact.
// All route writers lock the snapshot first.
func (s *Store) ActivateContainer(ctx context.Context, container Container, execution ContainerExecution, target string) (int64, error) {
	var version int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE").Row().Scan(&version); err != nil {
			return fmt.Errorf("lock route snapshot version: %w", err)
		}
		var current Container
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("project_id = ? AND id = ?", container.ProjectID, container.ID).Take(&current).Error; err != nil {
			return fmt.Errorf("lock container %s: %w", container.ID, err)
		}
		if current.ActiveExecutionID != nil {
			if *current.ActiveExecutionID == execution.ID {
				return nil
			}
			return ErrContainerAlreadyActive
		}
		var conflict bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM project_routes WHERE host = ? AND (project_id <> ? OR path_prefix = '/'))", execution.Hostname, execution.ProjectID).Row().Scan(&conflict); err != nil {
			return fmt.Errorf("check container host %q: %w", execution.Hostname, err)
		}
		if conflict {
			return ErrRouteConflict
		}
		result := tx.Model(&ContainerExecution{}).
			Where("project_id = ? AND id = ? AND state = 'pending'", execution.ProjectID, execution.ID).
			Updates(map[string]any{
				"docker_container_id": execution.DockerContainerID,
				"target":              target,
				"state":               "running",
				"failure":             "",
			})
		if result.Error != nil {
			return fmt.Errorf("activate container execution %s: %w", execution.ID, result.Error)
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("container execution %s is not pending", execution.ID)
		}
		if err := tx.Model(&Container{}).Where("project_id = ? AND id = ?", container.ProjectID, container.ID).
			Updates(map[string]any{"active_execution_id": execution.ID, "state": "active", "failure": ""}).Error; err != nil {
			return fmt.Errorf("activate container %s: %w", container.ID, err)
		}
		route := ProjectRoute{
			ID:         uuid.NewString(),
			ProjectID:  execution.ProjectID.String(),
			Resource:   container.ID.String(),
			Host:       execution.Hostname,
			PathPrefix: execution.PathPrefix,
			Target:     target,
		}
		if err := tx.Create(&route).Error; err != nil {
			return routeWriteError("activate container route", err)
		}
		return tx.Raw("UPDATE route_snapshot SET version = version + 1 WHERE id = 1 RETURNING version").Row().Scan(&version)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return version, err
}

// FailContainerExecution records a definite failure after container cleanup.
func (s *Store) FailContainerExecution(ctx context.Context, projectID, containerID, executionID uuid.UUID, cause error) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ContainerExecution{}).
			Where("project_id = ? AND id = ?", projectID, executionID).
			Updates(map[string]any{"state": "failed", "failure": cause.Error(), "target": ""}).Error; err != nil {
			return fmt.Errorf("fail container execution %s: %w", executionID, err)
		}
		if err := tx.Model(&Container{}).
			Where("project_id = ? AND id = ?", projectID, containerID).
			Updates(map[string]any{"state": "failed", "failure": cause.Error()}).Error; err != nil {
			return fmt.Errorf("fail container %s: %w", containerID, err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// RecordContainerOperationFailure preserves a failure that happened before any
// container execution row was written. The first recorded cause is kept so an
// idempotent retry returns the original result.
func (s *Store) RecordContainerOperationFailure(ctx context.Context, projectID, operationID uuid.UUID, cause error) error {
	if projectID == uuid.Nil || operationID == uuid.Nil {
		return fmt.Errorf("record container operation failure: project and operation IDs are required: %w", fs.ErrInvalid)
	}
	if cause == nil {
		return fmt.Errorf("record container operation failure: cause is required: %w", fs.ErrInvalid)
	}
	message := cause.Error()
	if message == "" {
		message = "container creation failed"
	}
	if err := s.DB.WithContext(ctx).Exec(
		"INSERT INTO container_operation_failures (operation_id, project_id, failure) VALUES (?, ?, ?) ON CONFLICT (operation_id) DO NOTHING",
		operationID, projectID, message,
	).Error; err != nil {
		return fmt.Errorf("record container operation %s failure: %w", operationID, err)
	}
	return nil
}

// ContainerOperationFailure returns a failure recorded before an execution row
// existed. found is false when the operation has no such record.
func (s *Store) ContainerOperationFailure(ctx context.Context, projectID, operationID uuid.UUID) (string, bool, error) {
	var failure string
	err := s.DB.WithContext(ctx).Raw(
		"SELECT failure FROM container_operation_failures WHERE project_id = ? AND operation_id = ?",
		projectID, operationID,
	).Row().Scan(&failure)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read container operation %s failure: %w", operationID, err)
	}
	return failure, true, nil
}

// MarkContainerStopped records a confirmed stop after the Docker container is
// removed and its capacity is released. The execution and container keep their
// terminal records for queries.
func (s *Store) MarkContainerStopped(ctx context.Context, projectID, containerID, executionID uuid.UUID) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ContainerExecution{}).
			Where("project_id = ? AND container_id = ? AND id = ?", projectID, containerID, executionID).
			Updates(map[string]any{"state": "stopped", "failure": "", "target": ""}).Error; err != nil {
			return fmt.Errorf("stop container execution %s: %w", executionID, err)
		}
		if err := tx.Model(&Container{}).
			Where("project_id = ? AND id = ?", projectID, containerID).
			Updates(map[string]any{"active_execution_id": nil, "state": "stopped", "failure": ""}).Error; err != nil {
			return fmt.Errorf("stop container %s: %w", containerID, err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// MarkContainerRestartFailed records a failed restart after cleanup. The
// container returns to stopped so a later restart can be attempted again.
func (s *Store) MarkContainerRestartFailed(ctx context.Context, projectID, containerID, executionID uuid.UUID, cause error) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&ContainerExecution{}).
			Where("project_id = ? AND container_id = ? AND id = ?", projectID, containerID, executionID).
			Updates(map[string]any{"state": "failed", "failure": cause.Error(), "target": ""}).Error; err != nil {
			return fmt.Errorf("fail container execution %s: %w", executionID, err)
		}
		if err := tx.Model(&Container{}).
			Where("project_id = ? AND id = ?", projectID, containerID).
			Updates(map[string]any{"active_execution_id": nil, "state": "stopped", "failure": cause.Error()}).Error; err != nil {
			return fmt.Errorf("fail container %s restart: %w", containerID, err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}

// DeleteContainer removes the logical container and all execution records after
// the route and Docker container are gone. Operations remain as audit records.
func (s *Store) DeleteContainer(ctx context.Context, projectID, containerID uuid.UUID) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Container{}).
			Where("project_id = ? AND id = ?", projectID, containerID).
			Update("active_execution_id", nil).Error; err != nil {
			return fmt.Errorf("clear active execution for container %s: %w", containerID, err)
		}
		if err := tx.Where("project_id = ? AND container_id = ?", projectID, containerID).
			Delete(&ContainerExecution{}).Error; err != nil {
			return fmt.Errorf("delete container %s executions: %w", containerID, err)
		}
		if err := tx.Where("project_id = ? AND id = ?", projectID, containerID).
			Delete(&Container{}).Error; err != nil {
			return fmt.Errorf("delete container %s: %w", containerID, err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
}
