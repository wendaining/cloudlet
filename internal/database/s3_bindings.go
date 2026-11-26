package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

type S3Binding struct {
	ProjectID       string
	Name            string
	Endpoint        string
	Region          string
	Bucket          string
	SecretVersionID uuid.UUID
	VerifiedAt      time.Time
}

func (S3Binding) TableName() string { return "s3_bindings" }

func (s *Store) CreateS3Binding(ctx context.Context, binding S3Binding) (bool, error) {
	result := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&binding)
	if result.Error != nil {
		return false, fmt.Errorf("create S3 binding %q in project %s: %w", binding.Name, binding.ProjectID, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func (s *Store) S3Binding(ctx context.Context, projectID, name string) (S3Binding, error) {
	var binding S3Binding
	if err := s.DB.WithContext(ctx).Where("project_id = ? AND name = ?", projectID, name).Take(&binding).Error; err != nil {
		return S3Binding{}, fmt.Errorf("read S3 binding %q in project %s: %w", name, projectID, err)
	}
	return binding, nil
}
