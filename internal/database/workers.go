package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrWorkerAlreadyActive = errors.New("Worker already has an active deployment")
var ErrWorkerPublicationPending = errors.New("Worker already has a pending or ready publication")
var ErrWorkerHostnameChanged = errors.New("Worker candidate must use the active hostname")
var ErrWorkerDeploymentNotReady = errors.New("Worker deployment is not ready for activation")

type Worker struct {
	ID                 uuid.UUID
	ProjectID          uuid.UUID
	Name               string
	ActiveDeploymentID *uuid.UUID
	CreatedAt          time.Time
}

// WorkerDeployment contains the immutable deployment inputs and its outcome.
type WorkerDeployment struct {
	ID                uuid.UUID
	ProjectID         uuid.UUID
	WorkerID          uuid.UUID
	OperationID       uuid.UUID
	ArtifactDigest    string
	ConfigDigest      string
	Entry             string
	CompatibilityDate string
	RuntimeVersion    string
	RuntimeImage      string
	Hostname          string
	CPUMillis         int64
	MemoryBytes       int64
	State             string
	Failure           string
	CreatedAt         time.Time
}

type WorkerExecution struct {
	ID           uuid.UUID
	ProjectID    uuid.UUID
	WorkerID     uuid.UUID
	DeploymentID uuid.UUID
	Generation   uint64
	ContainerID  string
	State        string
}

// CheckWorkerPublication validates the deployment before writing artifacts.
func (s *Store) CheckWorkerPublication(ctx context.Context, projectID uuid.UUID, name, hostname string, stage bool) error {
	db := s.DB.WithContext(ctx)
	var worker Worker
	err := db.Where("project_id = ? AND name = ?", projectID, name).Take(&worker).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if stage {
			return ErrWorkerDeploymentNotReady
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Worker %s/%s: %w", projectID, name, err)
	}
	return checkWorkerPublication(db, worker, hostname, stage)
}

func checkWorkerPublication(db *gorm.DB, worker Worker, hostname string, stage bool) error {
	if worker.ActiveDeploymentID != nil {
		if !stage {
			return ErrWorkerAlreadyActive
		}
		var active WorkerDeployment
		if err := db.Where("id = ? AND project_id = ?", *worker.ActiveDeploymentID, worker.ProjectID).Take(&active).Error; err != nil {
			return err
		}
		if active.Hostname != hostname {
			return ErrWorkerHostnameChanged
		}
	} else if stage {
		return ErrWorkerDeploymentNotReady
	}
	var pending bool
	err := db.Raw(`SELECT EXISTS (
		SELECT 1 FROM worker_deployments d JOIN worker_executions e ON e.deployment_id = d.id
		WHERE d.worker_id = ? AND (d.state IN ('pending', 'ready') OR (d.state = 'retired' AND e.state = 'running'))
	)`, worker.ID).Row().Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrWorkerPublicationPending
	}
	return nil
}

// CreateWorkerDeployment records a complete artifact before starting runtime work.
func (s *Store) CreateWorkerDeployment(ctx context.Context, name string, deployment WorkerDeployment, stage bool) (WorkerDeployment, WorkerExecution, error) {
	worker := Worker{ID: uuid.New(), ProjectID: deployment.ProjectID, Name: name}
	execution := WorkerExecution{ID: uuid.New(), ProjectID: deployment.ProjectID, DeploymentID: deployment.ID, State: "pending"}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "name"}}, DoNothing: true}).Create(&worker).Error; err != nil {
			return fmt.Errorf("create Worker %s/%s: %w", deployment.ProjectID, name, err)
		}
		worker = Worker{}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND name = ?", deployment.ProjectID, name).Take(&worker).Error; err != nil {
			return err
		}
		if err := checkWorkerPublication(tx, worker, deployment.Hostname, stage); err != nil {
			return err
		}
		deployment.WorkerID = worker.ID
		execution.WorkerID = worker.ID
		if err := tx.Raw("SELECT COALESCE(MAX(generation), 0) + 1 FROM worker_executions WHERE worker_id = ?", worker.ID).Row().Scan(&execution.Generation); err != nil {
			return err
		}
		if err := tx.Create(&deployment).Error; err != nil {
			return fmt.Errorf("record Worker deployment %s: %w", deployment.ID, err)
		}
		return tx.Create(&execution).Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return deployment, execution, err
}

func (s *Store) WorkerDeployment(ctx context.Context, projectID, deploymentID uuid.UUID) (WorkerDeployment, WorkerExecution, error) {
	var deployment WorkerDeployment
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND id = ?", projectID, deploymentID).Take(&deployment).Error; err != nil {
		return deployment, WorkerExecution{}, err
	}
	var execution WorkerExecution
	err := s.DB.WithContext(ctx).Where("project_id = ? AND deployment_id = ?", projectID, deploymentID).Take(&execution).Error
	return deployment, execution, err
}

func (s *Store) WorkerDeploymentByOperation(ctx context.Context, projectID, operationID uuid.UUID) (WorkerDeployment, WorkerExecution, error) {
	var deployment WorkerDeployment
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND operation_id = ?", projectID, operationID).Take(&deployment).Error; err != nil {
		return deployment, WorkerExecution{}, err
	}
	return s.WorkerDeployment(ctx, projectID, deployment.ID)
}

func (s *Store) SetWorkerExecutionContainer(ctx context.Context, projectID, executionID uuid.UUID, containerID string) error {
	result := s.DB.WithContext(ctx).Model(&WorkerExecution{}).Where("project_id = ? AND id = ? AND state = 'pending'", projectID, executionID).Update("container_id", containerID)
	if result.Error != nil {
		return fmt.Errorf("record Worker execution %s container %s: %w", executionID, containerID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("Worker execution %s is not pending", executionID)
	}
	return nil
}

// ActivateWorkerDeployment atomically commits the first active version and its route.
// Existing project routes remain intact. All route writers lock the snapshot first.
func (s *Store) ActivateWorkerDeployment(ctx context.Context, deployment WorkerDeployment, execution WorkerExecution, target string) (int64, error) {
	var version int64
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE").Row().Scan(&version); err != nil {
			return err
		}
		var worker Worker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND id = ?", deployment.ProjectID, deployment.WorkerID).Take(&worker).Error; err != nil {
			return err
		}
		if worker.ActiveDeploymentID != nil {
			return ErrWorkerAlreadyActive
		}
		var conflict bool
		if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM project_routes WHERE host = ? AND (project_id <> ? OR path_prefix = '/'))", deployment.Hostname, deployment.ProjectID).Row().Scan(&conflict); err != nil {
			return err
		}
		if conflict {
			return ErrRouteConflict
		}
		result := tx.Model(&WorkerDeployment{}).Where("id = ? AND project_id = ? AND state = 'pending'", deployment.ID, deployment.ProjectID).Update("state", "active")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("deployment %s is not pending", deployment.ID)
		}
		if err := tx.Model(&worker).Update("active_deployment_id", deployment.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&WorkerExecution{}).Where("id = ? AND deployment_id = ?", execution.ID, deployment.ID).Updates(map[string]any{"container_id": execution.ContainerID, "state": "running"}).Error; err != nil {
			return err
		}
		route := ProjectRoute{ID: uuid.NewString(), ProjectID: deployment.ProjectID.String(), Resource: deployment.WorkerID.String(), Host: deployment.Hostname, PathPrefix: "/", Target: target}
		if err := tx.Create(&route).Error; err != nil {
			return routeWriteError("activate Worker route", err)
		}
		return tx.Raw("UPDATE route_snapshot SET version = version + 1 WHERE id = 1 RETURNING version").Row().Scan(&version)
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return version, err
}

// FailWorkerDeployment preserves immutable inputs and the original failure.
func (s *Store) FailWorkerDeployment(ctx context.Context, deploymentID uuid.UUID, cause error, cleaned bool) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&WorkerDeployment{}).Where("id = ? AND state = 'pending'", deploymentID).Updates(map[string]any{"state": "failed", "failure": cause.Error()}).Error; err != nil {
			return err
		}
		if cleaned {
			return tx.Model(&WorkerExecution{}).Where("deployment_id = ?", deploymentID).Update("state", "stopped").Error
		}
		return nil
	})
}
