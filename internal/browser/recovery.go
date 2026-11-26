package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/wendaining/cloudlet/internal/database"
)

// Restore reconciles the persisted sessions for this controller before it starts
// serving requests. Valid executions stay in their original containers.
func (s *Service) Restore(ctx context.Context) error {
	sessions, err := s.store.RecoverableBrowserSessions(ctx, s.hostID)
	if err != nil {
		return fmt.Errorf("read recoverable browser sessions: %w", err)
	}
	for _, session := range sessions {
		if err := s.restoreSession(ctx, session); err != nil {
			return fmt.Errorf("restore browser session %s: %w", session.ID, err)
		}
	}
	return nil
}

func (s *Service) restoreSession(ctx context.Context, session database.BrowserSession) error {
	if !time.Now().Before(session.ExpiresAt) {
		_, err := s.finish(ctx, session, "closed")
		return err
	}
	endpoint, err := s.boundRuntimeEndpoint(ctx, session)
	if errors.Is(err, ErrSessionUnavailable) {
		_, err := s.finish(ctx, session, "failed")
		return err
	}
	if err != nil {
		return err
	}
	if err := s.verifyRuntime(ctx, session, endpoint); err != nil {
		if !time.Now().Before(session.ExpiresAt) {
			_, cleanupErr := s.finish(ctx, session, "closed")
			return cleanupErr
		}
		return err
	}
	if !time.Now().Before(session.ExpiresAt) {
		_, err := s.finish(ctx, session, "closed")
		return err
	}
	if session.Status == "starting" {
		return s.store.SetBrowserSessionStatus(ctx, session, "ready")
	}
	return nil
}

func (s *Service) verifyRuntime(ctx context.Context, session database.BrowserSession, endpoint string) error {
	ctx, cancel := context.WithTimeout(ctx, min(time.Until(session.ExpiresAt), 5*time.Second))
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/viewport", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+s.token(session.ID))
	response, err := s.http.Do(request)
	if err != nil {
		return fmt.Errorf("verify authenticated browser endpoint: %w", err)
	}
	defer response.Body.Close()
	// An existing command can still be running after the previous controller
	// disconnected. The runtime checks the credential before returning 409.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		return fmt.Errorf("verify authenticated browser endpoint: HTTP %d", response.StatusCode)
	}
	return nil
}
