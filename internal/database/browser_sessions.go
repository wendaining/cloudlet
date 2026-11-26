package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BrowserSession struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	HostID    string
	Status    string
	CreatedAt time.Time
	ExpiresAt time.Time
	LastError string
}

func (s *Store) CreateBrowserSession(ctx context.Context, session BrowserSession) error {
	if err := s.DB.WithContext(ctx).Create(&session).Error; err != nil {
		return fmt.Errorf("register browser session %s: %w", session.ID, err)
	}
	return nil
}

func (s *Store) BrowserSession(ctx context.Context, projectID, id uuid.UUID, hostID string) (BrowserSession, error) {
	var session BrowserSession
	err := s.DB.WithContext(ctx).Take(&session, "id = ? AND project_id = ? AND host_id = ?", id, projectID, hostID).Error
	return session, err
}

func (s *Store) RecoverableBrowserSessions(ctx context.Context, hostID string) ([]BrowserSession, error) {
	var sessions []BrowserSession
	err := s.DB.WithContext(ctx).Where("host_id = ? AND status IN ('starting', 'ready')", hostID).
		Order("expires_at, id").Find(&sessions).Error
	return sessions, err
}

func (s *Store) ExpiredBrowserSessions(ctx context.Context, hostID string, now time.Time) ([]BrowserSession, error) {
	var sessions []BrowserSession
	err := s.DB.WithContext(ctx).Where("host_id = ? AND expires_at <= ? AND status IN ('starting', 'ready')", hostID, now).
		Order("expires_at, id").Find(&sessions).Error
	return sessions, err
}

func (s *Store) SetBrowserSessionStatus(ctx context.Context, session BrowserSession, status string) error {
	result := s.DB.WithContext(ctx).Model(&BrowserSession{}).
		Where("id = ? AND project_id = ? AND host_id = ? AND status = ?", session.ID, session.ProjectID, session.HostID, session.Status).
		UpdateColumn("status", status)
	if result.Error != nil {
		return fmt.Errorf("update browser session %s status: %w", session.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *Store) ProjectBrowserSessions(ctx context.Context, projectID uuid.UUID, hostID string, before *uuid.UUID, limit int) ([]BrowserSession, error) {
	query := s.DB.WithContext(ctx).Where("project_id = ? AND host_id = ?", projectID, hostID)
	if before != nil {
		cursor, err := s.BrowserSession(ctx, projectID, *before, hostID)
		if err != nil {
			return nil, err
		}
		query = query.Where("(created_at, id) < (?, ?)", cursor.CreatedAt, cursor.ID)
	}
	var sessions []BrowserSession
	err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&sessions).Error
	return sessions, err
}

func (s *Store) RecordBrowserSessionError(ctx context.Context, projectID, id uuid.UUID, hostID, message string) error {
	result := s.DB.WithContext(ctx).Model(&BrowserSession{}).Where("project_id = ? AND id = ? AND host_id = ?", projectID, id, hostID).
		UpdateColumn("last_error", message)
	if result.Error != nil {
		return fmt.Errorf("record error for browser session %s: %w", id, result.Error)
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
