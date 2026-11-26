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
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond+2*time.Second)
	defer cancel()
	body, err := json.Marshal(struct {
		Selector string `json:"selector"`
		Timeout  int    `json:"timeout_ms"`
	}{Selector: selector, Timeout: timeout})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/click", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.token(id))
	response, err := s.http.Do(request)
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrClickTimeout
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrClickRuntime, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusBadRequest:
		return ErrClickInput
	case http.StatusConflict:
		return ErrSessionUnavailable
	case http.StatusUnprocessableEntity:
		return ErrClickFailed
	case http.StatusGatewayTimeout:
		return ErrClickTimeout
	default:
		return fmt.Errorf("%w: runtime HTTP %d", ErrClickRuntime, response.StatusCode)
	}
}
