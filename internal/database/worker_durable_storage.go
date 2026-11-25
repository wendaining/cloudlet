package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/durableobjects"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrWorkerStorageOwned = errors.New("Worker Durable Object storage already has an execution owner")

type WorkerDurableStorage struct {
	WorkerID         uuid.UUID `gorm:"primaryKey"`
	ProjectID        uuid.UUID
	VolumeName       string
	OwnerExecutionID *uuid.UUID
}

func (WorkerDurableStorage) TableName() string { return "worker_durable_storage" }

type WorkerDurableNamespace struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	WorkerID  uuid.UUID
	ClassName string
}

// ClaimWorkerDurableStorage assigns the storage to one pending execution.
func (s *Store) ClaimWorkerDurableStorage(ctx context.Context, execution WorkerExecution, namespaces []durableobjects.Namespace) (WorkerDurableStorage, error) {
	storage := WorkerDurableStorage{WorkerID: execution.WorkerID, ProjectID: execution.ProjectID, VolumeName: "cloudlet-do-" + execution.WorkerID.String()}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var pending WorkerExecution
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"id = ? AND project_id = ? AND worker_id = ? AND state = 'pending'", execution.ID, execution.ProjectID, execution.WorkerID,
		).Take(&pending).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&storage).Error; err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("project_id = ? AND worker_id = ?", execution.ProjectID, execution.WorkerID).Take(&storage).Error; err != nil {
			return err
		}
		if storage.OwnerExecutionID != nil && *storage.OwnerExecutionID != execution.ID {
			return fmt.Errorf("claim storage %s for execution %s: %w", storage.VolumeName, execution.ID, ErrWorkerStorageOwned)
		}
		for _, namespace := range namespaces {
			record := WorkerDurableNamespace{ID: namespace.ID, ProjectID: execution.ProjectID, WorkerID: execution.WorkerID, ClassName: namespace.ClassName}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoNothing: true}).Create(&record).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ? AND project_id = ? AND worker_id = ? AND class_name = ?", record.ID, record.ProjectID, record.WorkerID, record.ClassName).Take(&record).Error; err != nil {
				return err
			}
		}
		storage.OwnerExecutionID = &execution.ID
		return tx.Model(&storage).Update("owner_execution_id", execution.ID).Error
	})
	if err != nil {
		return WorkerDurableStorage{}, fmt.Errorf("register Durable Object storage for Worker %s: %w", execution.WorkerID, err)
	}
	return storage, nil
}
