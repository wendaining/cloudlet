package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
)

var ErrConsoleUnavailable = errors.New("无法读取浏览器控制台")

type ConsoleEntry struct {
	Kind      string `json:"kind"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Truncated bool   `json:"truncated"`
}

type ConsoleHistory struct {
	Entries []ConsoleEntry `json:"entries"`
	Dropped int64          `json:"dropped"`
}

func (s *Service) Console(ctx context.Context, projectID, id uuid.UUID) (ConsoleHistory, error) {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return ConsoleHistory{}, err
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return ConsoleHistory{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/console", nil)
	if err != nil {
		return ConsoleHistory{}, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token(id))
	response, err := s.http.Do(request)
	if err != nil {
		return ConsoleHistory{}, fmt.Errorf("%w: %w", ErrConsoleUnavailable, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ConsoleHistory{}, fmt.Errorf("%w: runtime HTTP %d", ErrConsoleUnavailable, response.StatusCode)
	}
	reader := &io.LimitedReader{R: response.Body, N: 512<<10 + 1}
	decoder := json.NewDecoder(reader)
	var history ConsoleHistory
	if err := decoder.Decode(&history); err != nil {
		return ConsoleHistory{}, fmt.Errorf("%w: decode runtime console: %w", ErrConsoleUnavailable, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || reader.N == 0 ||
		history.Entries == nil || len(history.Entries) > 100 || history.Dropped < 0 {
		return ConsoleHistory{}, ErrConsoleUnavailable
	}
	for _, entry := range history.Entries {
		if (entry.Kind != "console" && entry.Kind != "page_error") || entry.Level == "" || len(entry.Message) > 2048 {
			return ConsoleHistory{}, ErrConsoleUnavailable
		}
	}
	return history, nil
}
