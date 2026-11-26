package browser

import (
	"context"
	"errors"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
)

var (
	ErrFormInput   = errors.New("表单命令需要有效的操作、CSS 选择器、值和超时")
	ErrFormTimeout = errors.New("等待指定表单元素或选项超时")
	ErrFormFailed  = errors.New("无法唯一定位或操作指定表单元素")
	ErrFormRuntime = errors.New("浏览器表单命令失败")
)

type FormAction struct {
	Action   string `json:"action"`
	Selector string `json:"selector"`
	Value    string `json:"value"`
	Timeout  int    `json:"timeout_ms"`
}

func (action FormAction) valid() bool {
	return (action.Action == "fill" || action.Action == "select") &&
		validClick(action.Selector, action.Timeout) && len(utf16.Encode([]rune(action.Value))) <= 1024 &&
		!strings.ContainsRune(action.Value, 0)
}

func (s *Service) ApplyFormAction(ctx context.Context, projectID, id uuid.UUID, action FormAction) error {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return err
	}
	if !action.valid() {
		return ErrFormInput
	}
	return s.runPageAction(ctx, session, "/form", action, action.Timeout, pageActionErrors{
		ErrFormInput, ErrFormTimeout, ErrFormFailed, ErrFormRuntime,
	})
}
