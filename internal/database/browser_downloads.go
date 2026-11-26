package database

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type BrowserDownload struct {
	ID             uuid.UUID
	SessionID      uuid.UUID
	ArtifactDigest string
	SizeBytes      int64
	Filename       string
	CreatedAt      time.Time
}

func (s *Store) RegisterBrowserDownload(ctx context.Context, sessionID uuid.UUID, digest string, size int64, filename string) (BrowserDownload, error) {
	entry := BrowserDownload{ID: uuid.New(), SessionID: sessionID, ArtifactDigest: digest, SizeBytes: size, Filename: filename, CreatedAt: time.Now().UTC()}
	if err := s.DB.WithContext(ctx).Create(&entry).Error; err != nil {
		return BrowserDownload{}, err
	}
	return entry, nil
}

func (s *Store) BrowserDownload(ctx context.Context, projectID, sessionID, id uuid.UUID, hostID string) (BrowserDownload, error) {
	var entry BrowserDownload
	err := s.DB.WithContext(ctx).
		Joins("JOIN browser_sessions ON browser_sessions.id = browser_downloads.session_id").
		Where("browser_downloads.id = ? AND browser_downloads.session_id = ? AND browser_sessions.project_id = ? AND browser_sessions.host_id = ?", id, sessionID, projectID, hostID).
		Take(&entry).Error
	return entry, err
}
