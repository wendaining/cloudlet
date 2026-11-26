package capacity

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EnsureProjectQuota supplies the initial quota for a project and preserves an
// existing operator-configured limit.
func (s *Store) EnsureProjectQuota(ctx context.Context, projectID uuid.UUID, defaults Resources) error {
	if projectID == uuid.Nil {
		return fmt.Errorf("default project quota requires a project: %w", fs.ErrInvalid)
	}
	if err := defaults.validate(); err != nil {
		return err
	}
	project := projectQuotaRow{ProjectID: projectID, CPUMillis: defaults.CPUMillis, MemoryBytes: defaults.MemoryBytes, Instances: defaults.Instances, UpdatedAt: currentTime()}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&project).Error; err != nil {
		return fmt.Errorf("initialize project %s quota: %w", projectID, err)
	}
	return nil
}

// EnsureDefaults supplies capacity for a new host or project and preserves
// existing limits configured by an operator.
func (s *Store) EnsureDefaults(ctx context.Context, hostID string, projectID uuid.UUID, defaults Resources) error {
	if err := validateHostID(hostID); err != nil {
		return err
	}
	if projectID == uuid.Nil {
		return fmt.Errorf("default capacity requires a project: %w", fs.ErrInvalid)
	}
	if err := defaults.validate(); err != nil {
		return err
	}
	now := currentTime()
	host := hostCapacityRow{ID: hostID, CPUMillis: defaults.CPUMillis, MemoryBytes: defaults.MemoryBytes, Instances: defaults.Instances, UpdatedAt: now}
	project := projectQuotaRow{ProjectID: projectID, CPUMillis: defaults.CPUMillis, MemoryBytes: defaults.MemoryBytes, Instances: defaults.Instances, UpdatedAt: now}
	return s.writeTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&host).Error; err != nil {
			return fmt.Errorf("initialize host %s capacity: %w", hostID, err)
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&project).Error; err != nil {
			return fmt.Errorf("initialize project %s quota: %w", projectID, err)
		}
		return nil
	})
}
