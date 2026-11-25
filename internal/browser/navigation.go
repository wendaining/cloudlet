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
	if session.Status != "ready" {
		return Navigation{}, ErrSessionUnavailable
	}
	bound, err := s.capacity.GetReservation(ctx, projectID, id)
	if errors.Is(err, capacity.ErrReservationNotFound) {
		return Navigation{}, ErrSessionUnavailable
	}
	if err != nil {
		return Navigation{}, err
	}
	if bound.ContainerID == "" {
		return Navigation{}, ErrSessionUnavailable
	}
	observation, err := s.adapter.Observe(ctx, ownership(session), bound.ContainerID)
	if errors.Is(err, containers.ErrNotFound) {
		return Navigation{}, ErrSessionUnavailable
	}
	if err != nil {
		return Navigation{}, err
	}
	if !observation.Running {
		return Navigation{}, ErrSessionUnavailable
	}
	for _, port := range observation.Ports {
		if port.Protocol == "tcp" && port.ContainerPort == runtimePort {
			return s.navigate(ctx, "http://"+port.HostAddress()+"/navigate", id, value)
		}
	}
	return Navigation{}, ErrSessionUnavailable
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
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Navigation{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.token(id))
	response, err := s.http.Do(request)
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
