package browser

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
	"gorm.io/gorm"
)

const reapInterval = time.Second

// CloseSession stops a project session and returns its persisted terminal state.
// Once accepted, cleanup continues for a bounded interval if the client disconnects.
func (s *Service) CloseSession(ctx context.Context, projectID, id uuid.UUID) (database.BrowserSession, error) {
	session, err := s.store.BrowserSession(ctx, projectID, id, s.hostID)
	if err != nil {
		return database.BrowserSession{}, err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return s.closeSession(cleanupCtx, session)
}

// RunReaper checks deadlines while the controller is running. Chromium also
// receives the absolute deadline and can exit while the controller is offline.
func (s *Service) RunReaper(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.reapExpired(ctx); err != nil && ctx.Err() == nil {
				logger.Error("reap expired browser sessions", "error", err)
			}
		}
	}
}

func (s *Service) reapExpired(ctx context.Context) error {
	queryCtx, cancelQuery := context.WithTimeout(ctx, 5*time.Second)
	sessions, err := s.store.ExpiredBrowserSessions(queryCtx, s.hostID, time.Now().UTC())
	cancelQuery()
	if err != nil {
		return fmt.Errorf("read expired browser sessions: %w", err)
	}
	var failures error
	for _, session := range sessions {
		cleanupCtx, cancel := context.WithTimeout(ctx, cleanupTimeout)
		_, err := s.closeSession(cleanupCtx, session)
		cancel()
		if err != nil {
			failures = errors.Join(failures, err)
		}
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
	}
	return failures
}

func (s *Service) closeSession(ctx context.Context, session database.BrowserSession) (database.BrowserSession, error) {
	if terminalSession(session) {
		return session, nil
	}
	if done, starting := s.starting.Load(session.ID); starting {
		// Creation publishes the record before reserving capacity. Wait until
		// startup finishes so absence of a reservation can prove no execution.
		select {
		case <-done.(chan struct{}):
		case <-ctx.Done():
			return database.BrowserSession{}, ctx.Err()
		}
		current, err := s.store.BrowserSession(ctx, session.ProjectID, session.ID, s.hostID)
		if err != nil {
			return database.BrowserSession{}, err
		}
		session = current
	}
	return s.finish(ctx, session, "closed")
}

func (s *Service) finish(ctx context.Context, session database.BrowserSession, status string) (database.BrowserSession, error) {
	if terminalSession(session) {
		return session, nil
	}
	reservation, err := s.capacity.GetReservation(ctx, session.ProjectID, session.ID)
	if err == nil {
		err = s.executions.Stop(ctx, executions.StopRequest{
			ExecutionID: session.ID, ProjectID: session.ProjectID,
			Ownership: ownership(session), ContainerID: reservation.ContainerID,
		})
	}
	if err != nil && !errors.Is(err, capacity.ErrReservationNotFound) {
		return database.BrowserSession{}, fmt.Errorf("clean up browser session %s: %w", session.ID, err)
	}

	// Stop only releases capacity once removal is confirmed. The database
	// transition can be retried after a disconnect; a recorded release is idempotent.
	for {
		err := s.store.SetBrowserSessionStatus(ctx, session, status)
		if err == nil {
			session.Status = status
			return session, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return database.BrowserSession{}, err
		}
		current, lookupErr := s.store.BrowserSession(ctx, session.ProjectID, session.ID, session.HostID)
		if lookupErr != nil {
			return database.BrowserSession{}, errors.Join(err, lookupErr)
		}
		if terminalSession(current) {
			return current, nil
		}
		session = current
	}
}

func terminalSession(session database.BrowserSession) bool {
	return session.Status == "closed" || session.Status == "failed"
}
