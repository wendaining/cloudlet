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

var (
	ErrPageNotFound = errors.New("浏览器页面不存在")
	ErrPageConflict = errors.New("浏览器页面容量不足或会话正在执行其他命令")
	ErrPageTimeout  = errors.New("浏览器页面操作超时")
	ErrPageRuntime  = errors.New("浏览器页面操作失败")
)

type Page struct {
	ID    uuid.UUID `json:"id"`
	URL   string    `json:"url"`
	Title string    `json:"title"`
}

func (s *Service) OpenPage(ctx context.Context, projectID, sessionID uuid.UUID) (Page, error) {
	return s.pageRequest(ctx, projectID, sessionID, http.MethodPost, nil)
}

func (s *Service) DescribePage(ctx context.Context, projectID, sessionID, pageID uuid.UUID) (Page, error) {
	return s.pageRequest(ctx, projectID, sessionID, http.MethodGet, &pageID)
}

func (s *Service) ClosePage(ctx context.Context, projectID, sessionID, pageID uuid.UUID) error {
	_, err := s.pageRequest(ctx, projectID, sessionID, http.MethodDelete, &pageID)
	return err
}

func (s *Service) pageRequest(ctx context.Context, projectID, sessionID uuid.UUID, method string, pageID *uuid.UUID) (Page, error) {
	session, err := s.Session(ctx, projectID, sessionID)
	if err != nil {
		return Page{}, err
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return Page{}, err
	}
	endpoint += "/pages"
	if pageID != nil {
		endpoint += "/" + pageID.String()
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return Page{}, err
	}
	request.Header.Set("Authorization", "Bearer "+s.token(sessionID))
	response, err := s.http.Do(request)
	if errors.Is(err, context.DeadlineExceeded) {
		return Page{}, ErrPageTimeout
	}
	if err != nil {
		return Page{}, fmt.Errorf("%w: %w", ErrPageRuntime, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound:
		return Page{}, ErrPageNotFound
	case http.StatusConflict:
		return Page{}, ErrPageConflict
	case http.StatusGatewayTimeout:
		return Page{}, ErrPageTimeout
	}
	if method == http.MethodDelete && response.StatusCode == http.StatusNoContent {
		return Page{}, nil
	}
	if (method != http.MethodPost || response.StatusCode != http.StatusCreated) &&
		(method != http.MethodGet || response.StatusCode != http.StatusOK) {
		return Page{}, fmt.Errorf("%w: runtime HTTP %d", ErrPageRuntime, response.StatusCode)
	}
	reader := &io.LimitedReader{R: response.Body, N: 64<<10 + 1}
	decoder := json.NewDecoder(reader)
	var page Page
	if err := decoder.Decode(&page); err != nil {
		return Page{}, fmt.Errorf("%w: %w", ErrPageRuntime, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) || reader.N == 0 ||
		page.ID == uuid.Nil || (pageID != nil && page.ID != *pageID) ||
		(page.URL != "about:blank" && !validURL(page.URL)) {
		return Page{}, ErrPageRuntime
	}
	return page, nil
}
