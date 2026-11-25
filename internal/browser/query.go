package browser

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
)

const (
	DefaultSessionLimit = 50
	MaxSessionLimit     = 100
)

var ErrSessionListLimit = errors.New("每页会话数必须在 1 至 100 之间")

type SessionView struct {
	database.BrowserSession
	DesiredStatus string
	ActualStatus  string
}

// Describe reports the recorded state and the inspected runtime separately.
func (s *Service) Describe(ctx context.Context, session database.BrowserSession) (SessionView, error) {
	view := SessionView{BrowserSession: session, DesiredStatus: "ready", ActualStatus: session.Status}
	expired := !time.Now().Before(session.ExpiresAt)
	if expired || session.Status == "closed" || session.Status == "failed" {
		view.DesiredStatus = "closed"
	}
	if session.Status != "ready" {
		return view, nil
	}
	if _, err := s.runtimeEndpoint(ctx, session); err != nil {
		if !errors.Is(err, ErrSessionUnavailable) {
			return SessionView{}, err
		}
		view.ActualStatus = "closed"
		if !expired {
			view.ActualStatus = "failed"
			if view.LastError == "" {
				view.LastError = "浏览器运行时不可用"
			}
		}
	}
	return view, nil
}

func (s *Service) List(ctx context.Context, projectID uuid.UUID, before *uuid.UUID, limit int) ([]SessionView, *uuid.UUID, error) {
	if limit < 1 || limit > MaxSessionLimit {
		return nil, nil, ErrSessionListLimit
	}
	sessions, err := s.store.ProjectBrowserSessions(ctx, projectID, s.hostID, before, limit+1)
	if err != nil {
		return nil, nil, err
	}
	var next *uuid.UUID
	if len(sessions) > limit {
		sessions = sessions[:limit]
		lastID := sessions[len(sessions)-1].ID
		next = &lastID
	}
	views := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		view, err := s.Describe(ctx, session)
		if err != nil {
			return nil, nil, err
		}
		views = append(views, view)
	}
	return views, next, nil
}
