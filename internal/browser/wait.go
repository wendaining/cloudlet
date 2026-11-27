package browser

import (
	"context"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
)

var (
	ErrWaitInput   = errors.New("等待需要有效的页面条件、文本和超时")
	ErrWaitTimeout = errors.New("等待页面条件超时")
	ErrWaitFailed  = errors.New("等待期间无法读取浏览器页面")
	ErrWaitRuntime = errors.New("浏览器等待命令失败")
)

type PageWait struct {
	Condition string  `json:"condition"`
	Text      *string `json:"text,omitempty"`
	Timeout   int     `json:"timeout_ms"`
}

func (input PageWait) valid() bool {
	if input.Timeout < 1 || input.Timeout > 30000 {
		return false
	}
	switch input.Condition {
	case "ready":
		return input.Text == nil
	case "text":
		return input.Text != nil && strings.TrimSpace(*input.Text) != "" &&
			len(utf16.Encode([]rune(*input.Text))) <= 1024 && !strings.ContainsRune(*input.Text, 0)
	default:
		return false
	}
}

func (s *Service) Wait(ctx context.Context, projectID, id uuid.UUID, input PageWait) error {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return err
	}
	if !input.valid() {
		return ErrWaitInput
	}
	return s.runPageAction(ctx, session, "/wait", input, input.Timeout, pageActionErrors{
		ErrWaitInput, ErrWaitTimeout, ErrWaitFailed, ErrWaitRuntime,
	})
}
