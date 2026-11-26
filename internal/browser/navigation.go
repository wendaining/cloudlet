package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
)

var (
	ErrInvalidURL        = errors.New("导航需要不带凭据的 HTTP 或 HTTPS 地址")
	ErrNavigation        = errors.New("浏览器导航失败")
	ErrNavigationTimeout = errors.New("浏览器导航超时")
)

type Navigation struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func validURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && len(value) <= 4096 && parsed.Hostname() != "" && parsed.User == nil &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func (s *Service) Navigate(ctx context.Context, projectID, id uuid.UUID, value string) (Navigation, error) {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return Navigation{}, err
	}
	if !validURL(value) {
		return Navigation{}, ErrInvalidURL
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return Navigation{}, err
	}
	page, err := s.navigate(ctx, endpoint+"/navigate", id, value)
	if errors.Is(err, ErrNavigationTimeout) || errors.Is(err, ErrNavigation) {
		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		message := ErrNavigation.Error()
		if errors.Is(err, ErrNavigationTimeout) {
			message = ErrNavigationTimeout.Error()
		}
		if recordErr := s.store.RecordBrowserSessionError(recordCtx, projectID, id, s.hostID, message); recordErr != nil {
			return Navigation{}, errors.Join(err, fmt.Errorf("record browser navigation failure: %w", recordErr))
		}
	}
	return page, err
}

func (s *Service) runtimeEndpoint(ctx context.Context, session database.BrowserSession) (string, error) {
	if session.Status != "ready" {
		return "", ErrSessionUnavailable
	}
	return s.boundRuntimeEndpoint(ctx, session)
}

func (s *Service) boundRuntimeEndpoint(ctx context.Context, session database.BrowserSession) (string, error) {
	bound, err := s.capacity.GetReservation(ctx, session.ProjectID, session.ID)
	if errors.Is(err, capacity.ErrReservationNotFound) {
		return "", ErrSessionUnavailable
	}
	if err != nil {
		return "", err
	}
	if bound.State != capacity.Active || bound.ContainerID == "" {
		return "", ErrSessionUnavailable
	}
	owner := ownership(session)
	if bound.HostID != session.HostID || bound.Binding.Environment != owner.Environment ||
		bound.Binding.Project != owner.Project || bound.Binding.Resource != owner.Resource ||
		bound.Binding.Generation != owner.Generation || bound.Binding.Image == "" {
		return "", fmt.Errorf("verify browser session %s reservation: %w", session.ID, executions.ErrExecutionConflict)
	}
	observation, err := s.adapter.Observe(ctx, owner, bound.ContainerID)
	if errors.Is(err, containers.ErrNotFound) {
		return "", ErrSessionUnavailable
	}
	if err != nil {
		return "", err
	}
	if observation.Image != bound.Binding.Image {
		return "", fmt.Errorf("verify browser session %s container image: %w", session.ID, executions.ErrExecutionConflict)
	}
	if !observation.Running {
		return "", ErrSessionUnavailable
	}
	for _, port := range observation.Ports {
		if port.Protocol == "tcp" && port.ContainerPort == runtimePort {
			return "http://" + port.HostAddress(), nil
		}
	}
	return "", ErrSessionUnavailable
}

func (s *Service) navigate(ctx context.Context, endpoint string, id uuid.UUID, value string) (Navigation, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		URL string `json:"url"`
	}{value})
	if err != nil {
		return Navigation{}, err
	}
	response, err := s.postRuntime(ctx, endpoint, id, bytes.NewReader(body))
	if errors.Is(err, context.DeadlineExceeded) {
		return Navigation{}, ErrNavigationTimeout
	}
	if err != nil {
		return Navigation{}, fmt.Errorf("%w: %w", ErrNavigation, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		var result Navigation
		reader := &io.LimitedReader{R: response.Body, N: 64<<10 + 1}
		decoder := json.NewDecoder(reader)
		if err := decoder.Decode(&result); err != nil {
			return Navigation{}, fmt.Errorf("decode browser navigation: %w: %w", ErrNavigation, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || reader.N == 0 || !validURL(result.URL) {
			return Navigation{}, ErrNavigation
		}
		return result, nil
	case http.StatusBadRequest:
		return Navigation{}, ErrInvalidURL
	case http.StatusConflict, http.StatusGone:
		return Navigation{}, ErrSessionUnavailable
	case http.StatusGatewayTimeout:
		return Navigation{}, ErrNavigationTimeout
	default:
		return Navigation{}, fmt.Errorf("%w: runtime HTTP %d", ErrNavigation, response.StatusCode)
	}
}

func (s *Service) postRuntime(ctx context.Context, endpoint string, id uuid.UUID, body io.Reader) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+s.token(id))
	return s.http.Do(request)
}
