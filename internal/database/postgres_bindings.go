package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

type PostgresBinding struct {
	ProjectID       string
	Name            string
	Host            string
	Port            int
	Database        string
	SecretVersionID uuid.UUID
	VerifiedAt      time.Time
}

func (s *Store) CreatePostgresBinding(ctx context.Context, binding PostgresBinding) (bool, error) {
	result := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&binding)
	if result.Error != nil {
		return false, fmt.Errorf("create PostgreSQL binding %q in project %s: %w", binding.Name, binding.ProjectID, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (s *Store) PostgresBinding(ctx context.Context, projectID, name string) (PostgresBinding, error) {
	var binding PostgresBinding
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND name = ?", projectID, name).Take(&binding).Error; err != nil {
		return PostgresBinding{}, fmt.Errorf("read PostgreSQL binding %q in project %s: %w", name, projectID, err)
	}
	return binding, nil
}
