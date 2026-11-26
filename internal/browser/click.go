package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
)

var (
	ErrClickInput   = errors.New("点击需要有效的 CSS 选择器和 1 至 30000 毫秒的超时")
	ErrClickTimeout = errors.New("点击超时")
	ErrClickFailed  = errors.New("无法点击指定元素")
	ErrClickRuntime = errors.New("浏览器点击命令失败")
)

func validClick(selector string, timeout int) bool {
	return strings.TrimSpace(selector) != "" && len(utf16.Encode([]rune(selector))) <= 1024 &&
		!strings.ContainsFunc(selector, unicode.IsControl) && timeout >= 1 && timeout <= 30000
}

func (s *Service) Click(ctx context.Context, projectID, id uuid.UUID, selector string, timeout int) error {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return err
	}
	if !validClick(selector, timeout) {
		return ErrClickInput
	}
	return s.runPageAction(ctx, session, "/click", struct {
		Selector string `json:"selector"`
		Timeout  int    `json:"timeout_ms"`
	}{Selector: selector, Timeout: timeout}, timeout, pageActionErrors{
		ErrClickInput, ErrClickTimeout, ErrClickFailed, ErrClickRuntime,
	})
}

type pageActionErrors struct {
	input, timeout, failed, runtime error
}

func (s *Service) runPageAction(ctx context.Context, session database.BrowserSession, path string, input any, timeout int, result pageActionErrors) error {
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if len(body) > 8192 {
		return result.input
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond+2*time.Second)
	defer cancel()
	response, err := s.postRuntime(ctx, endpoint+path, session.ID, bytes.NewReader(body))
	if errors.Is(err, context.DeadlineExceeded) {
		return result.timeout
	}
	if err != nil {
		return fmt.Errorf("%w: %w", result.runtime, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusBadRequest:
		return result.input
	case http.StatusConflict:
		return ErrSessionUnavailable
	case http.StatusUnprocessableEntity:
		return result.failed
	case http.StatusGatewayTimeout:
		return result.timeout
	default:
		return fmt.Errorf("%w: runtime HTTP %d", result.runtime, response.StatusCode)
	}
}
