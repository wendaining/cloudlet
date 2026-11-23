package database

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrProjectOwner = errors.New("不能移除项目所有者")

func (s *Store) RemoveProjectMember(ctx context.Context, projectID, userID string) error {
	result := s.DB.WithContext(ctx).Where("project_id = ? AND user_id = ? AND role <> ?", projectID, userID, "owner").Delete(&ProjectMember{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var member ProjectMember
	err := s.DB.WithContext(ctx).Where("project_id = ? AND user_id = ?", projectID, userID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return gorm.ErrRecordNotFound
	}
	if err != nil {
		return err
	}
	if member.Role == "owner" {
		return ErrProjectOwner
	}
	return gorm.ErrRecordNotFound
}
