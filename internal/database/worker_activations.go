package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type WorkerActivation struct {
	OperationID          uuid.UUID
	ProjectID            uuid.UUID
	WorkerID             uuid.UUID
	DeploymentID         uuid.UUID
	PreviousDeploymentID uuid.UUID
	PreviousTarget       string
}

func (s *Store) ReadyWorkerDeployment(ctx context.Context, deployment WorkerDeployment, execution WorkerExecution) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := changeWorkerDeploymentState(tx, deployment.ProjectID, deployment.ID, "pending", "ready"); err != nil {
			return err
		}
		result := tx.Model(&WorkerExecution{}).
			Where("project_id = ? AND deployment_id = ? AND id = ? AND state = 'pending' AND container_id = ?", deployment.ProjectID, deployment.ID, execution.ID, execution.ContainerID).
			Update("state", "running")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("Worker execution %s is not pending with its recorded container", execution.ID)
		}
		return nil
	})
}

func (s *Store) WorkerActivationByDeployment(ctx context.Context, projectID, deploymentID uuid.UUID) (WorkerActivation, error) {
	var activation WorkerActivation
	err := s.DB.WithContext(ctx).Where("project_id = ? AND deployment_id = ?", projectID, deploymentID).Take(&activation).Error
	return activation, err
}

// SwitchWorkerDeployment commits the route, current version and drain record together.
func (s *Store) SwitchWorkerDeployment(ctx context.Context, deployment WorkerDeployment, operationID uuid.UUID, target string) (WorkerActivation, error) {
	activation := WorkerActivation{OperationID: operationID, ProjectID: deployment.ProjectID, WorkerID: deployment.WorkerID, DeploymentID: deployment.ID}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot int64
		if err := tx.Raw("SELECT version FROM route_snapshot WHERE id = 1 FOR UPDATE").Row().Scan(&snapshot); err != nil {
			return err
		}
		var worker Worker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND id = ?", deployment.ProjectID, deployment.WorkerID).Take(&worker).Error; err != nil {
			return err
		}
		if worker.ActiveDeploymentID == nil || *worker.ActiveDeploymentID == deployment.ID {
			return ErrWorkerDeploymentNotReady
		}
		activation.PreviousDeploymentID = *worker.ActiveDeploymentID
		var route ProjectRoute
		err := tx.Where("project_id = ? AND resource = ? AND host = ? AND path_prefix = '/' AND static_site_version_id IS NULL",
			deployment.ProjectID, deployment.WorkerID, deployment.Hostname).Take(&route).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("Worker %s has no owned root route for %s: %w", deployment.WorkerID, deployment.Hostname, ErrRouteConflict)
		}
		if err != nil {
			return err
		}
		activation.PreviousTarget = route.Target
		if err := changeWorkerDeploymentState(tx, deployment.ProjectID, activation.PreviousDeploymentID, "active", "retired"); err != nil {
			return err
		}
		if err := changeWorkerDeploymentState(tx, deployment.ProjectID, deployment.ID, "ready", "active"); err != nil {
			return err
		}
		if err := tx.Model(&worker).Update("active_deployment_id", deployment.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&route).Update("target", target).Error; err != nil {
			return routeWriteError("switch Worker route", err)
		}
		if err := tx.Create(&activation).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE route_snapshot SET version = version + 1 WHERE id = 1").Error
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	return activation, err
}

func changeWorkerDeploymentState(tx *gorm.DB, projectID, deploymentID uuid.UUID, previous, next string) error {
	result := tx.Model(&WorkerDeployment{}).Where("project_id = ? AND id = ? AND state = ?", projectID, deploymentID, previous).Update("state", next)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("Worker deployment %s is not %s: %w", deploymentID, previous, ErrWorkerDeploymentNotReady)
	}
	return nil
}

func (s *Store) StopRetiredWorkerExecution(ctx context.Context, projectID, executionID uuid.UUID) error {
	result := s.DB.WithContext(ctx).Exec(`UPDATE worker_executions AS e SET state = 'stopped'
		FROM worker_deployments AS d WHERE e.project_id = ? AND e.id = ?
		AND d.id = e.deployment_id AND d.project_id = e.project_id AND d.state = 'retired'`, projectID, executionID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("Worker execution %s is not retired", executionID)
	}
	return nil
}
