package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type BrowserScreenshot struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	Digest    string
	SizeBytes int64
	CreatedAt time.Time
}

func (s *Store) RegisterBrowserScreenshot(ctx context.Context, sessionID uuid.UUID, digest string, size int64) (BrowserScreenshot, error) {
	screenshot := BrowserScreenshot{ID: uuid.New(), SessionID: sessionID, Digest: digest, SizeBytes: size}
	if err := s.DB.WithContext(ctx).Create(&screenshot).Error; err != nil {
		return BrowserScreenshot{}, fmt.Errorf("register browser screenshot for session %s: %w", sessionID, err)
	}
	return screenshot, nil
}

func (s *Store) BrowserScreenshot(ctx context.Context, projectID, sessionID, screenshotID uuid.UUID, hostID string) (BrowserScreenshot, error) {
	var screenshot BrowserScreenshot
	err := s.DB.WithContext(ctx).
		Joins("JOIN browser_sessions ON browser_sessions.id = browser_screenshots.session_id").
		Where("browser_sessions.project_id = ? AND browser_sessions.host_id = ? AND browser_screenshots.session_id = ? AND browser_screenshots.id = ?",
			projectID, hostID, sessionID, screenshotID).
		Take(&screenshot).Error
	return screenshot, err
}
