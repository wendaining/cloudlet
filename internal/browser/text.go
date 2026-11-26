package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/google/uuid"
)

var (
	ErrTextScope        = errors.New("无效的文本范围 CSS 选择器")
	ErrTextScopeMissing = errors.New("页面或指定文本范围不可用")
	ErrTextTimeout      = errors.New("读取页面文本超时")
	ErrTextRuntime      = errors.New("无法读取页面文本")
)

type PageText struct {
	Title          string `json:"title"`
	TitleTruncated bool   `json:"title_truncated"`
	Text           string `json:"text"`
	TextTruncated  bool   `json:"text_truncated"`
}

func validTextScope(selector string) bool {
	return strings.TrimSpace(selector) != "" && len(utf16.Encode([]rune(selector))) <= 1024 &&
		!strings.ContainsFunc(selector, unicode.IsControl)
}

func (s *Service) Text(ctx context.Context, projectID, id uuid.UUID, selector *string) (PageText, error) {
	session, err := s.Session(ctx, projectID, id)
	if err != nil {
		return PageText{}, err
	}
	query := ""
	if selector != nil {
		if !validTextScope(*selector) {
			return PageText{}, ErrTextScope
		}
		query = "?" + url.Values{"selector": {*selector}}.Encode()
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return PageText{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/text"+query, nil)
	if err != nil {
		return PageText{}, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token(id))
	response, err := s.http.Do(request)
	if errors.Is(err, context.DeadlineExceeded) {
		return PageText{}, ErrTextTimeout
	}
	if err != nil {
		return PageText{}, fmt.Errorf("%w: %w", ErrTextRuntime, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		reader := &io.LimitedReader{R: response.Body, N: 32<<10 + 1}
		decoder := json.NewDecoder(reader)
		var text PageText
		if err := decoder.Decode(&text); err != nil {
			return PageText{}, fmt.Errorf("%w: %w", ErrTextRuntime, err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || reader.N == 0 ||
			len(utf16.Encode([]rune(text.Title))) > 512 || len(utf16.Encode([]rune(text.Text))) > 4096 {
			return PageText{}, ErrTextRuntime
		}
		return text, nil
	case http.StatusBadRequest:
		return PageText{}, ErrTextScope
	case http.StatusConflict:
		return PageText{}, ErrTextScopeMissing
	case http.StatusGatewayTimeout:
		return PageText{}, ErrTextTimeout
	default:
		return PageText{}, fmt.Errorf("%w: runtime HTTP %d", ErrTextRuntime, response.StatusCode)
	}
}
