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
	"os"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
)

const maxDownloadBytes = 8 << 20

var (
	ErrDownloadInput    = errors.New("下载需要有效的 CSS 选择器和超时")
	ErrDownloadRejected = errors.New("下载失败或文件超过 8 MiB")
	ErrDownloadTimeout  = errors.New("等待页面下载超时")
	ErrDownloadRuntime  = errors.New("浏览器下载命令失败")
)

func downloadFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." || name == "/" {
		return "browser-download"
	}
	if runes := []rune(name); len(runes) > 128 {
		return string(runes[:128])
	}
	return name
}

func (s *Service) CaptureDownload(ctx context.Context, projectID, sessionID uuid.UUID, selector string, timeout int) (database.BrowserDownload, error) {
	session, err := s.Session(ctx, projectID, sessionID)
	if err != nil {
		return database.BrowserDownload{}, err
	}
	if !validClick(selector, timeout) {
		return database.BrowserDownload{}, ErrDownloadInput
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return database.BrowserDownload{}, err
	}
	body, err := json.Marshal(struct {
		Selector string `json:"selector"`
		Timeout  int    `json:"timeout_ms"`
	}{selector, timeout})
	if err != nil {
		return database.BrowserDownload{}, err
	}
	downloadCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond+2*time.Second)
	defer cancel()
	response, err := s.postRuntime(downloadCtx, endpoint+"/download", sessionID, bytes.NewReader(body))
	if errors.Is(err, context.DeadlineExceeded) {
		return database.BrowserDownload{}, ErrDownloadTimeout
	}
	if err != nil {
		return database.BrowserDownload{}, fmt.Errorf("%w: %w", ErrDownloadRuntime, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		return database.BrowserDownload{}, ErrDownloadInput
	case http.StatusConflict:
		return database.BrowserDownload{}, ErrSessionUnavailable
	case http.StatusUnprocessableEntity:
		return database.BrowserDownload{}, ErrDownloadRejected
	case http.StatusGatewayTimeout:
		return database.BrowserDownload{}, ErrDownloadTimeout
	default:
		return database.BrowserDownload{}, fmt.Errorf("%w: runtime HTTP %d", ErrDownloadRuntime, response.StatusCode)
	}
	if response.Header.Get("Content-Type") != "application/octet-stream" {
		return database.BrowserDownload{}, ErrDownloadRuntime
	}
	if response.ContentLength > maxDownloadBytes {
		return database.BrowserDownload{}, ErrDownloadRejected
	}
	rawName, err := url.PathUnescape(response.Header.Get("X-Cloudlet-Download-Name"))
	if err != nil {
		return database.BrowserDownload{}, fmt.Errorf("%w: %w", ErrDownloadRuntime, err)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDownloadBytes+1))
	if errors.Is(err, context.DeadlineExceeded) {
		return database.BrowserDownload{}, ErrDownloadTimeout
	}
	if err != nil {
		return database.BrowserDownload{}, fmt.Errorf("%w: %w", ErrDownloadRuntime, err)
	}
	if len(data) > maxDownloadBytes {
		return database.BrowserDownload{}, ErrDownloadRejected
	}
	if err := downloadCtx.Err(); err != nil {
		return database.BrowserDownload{}, err
	}
	digest, err := s.downloads.Put(bytes.NewReader(data))
	if err != nil {
		return database.BrowserDownload{}, fmt.Errorf("store browser download: %w", err)
	}
	return s.store.RegisterBrowserDownload(ctx, sessionID, digest, int64(len(data)), downloadFilename(rawName))
}

func (s *Service) OpenDownload(ctx context.Context, projectID, sessionID, id uuid.UUID) (database.BrowserDownload, *os.File, error) {
	entry, err := s.store.BrowserDownload(ctx, projectID, sessionID, id, s.hostID)
	if err != nil {
		return database.BrowserDownload{}, nil, err
	}
	file, err := s.downloads.Open(entry.ArtifactDigest)
	if err != nil {
		return database.BrowserDownload{}, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return database.BrowserDownload{}, nil, errors.Join(err, file.Close())
	}
	if info.Size() != entry.SizeBytes {
		return database.BrowserDownload{}, nil, errors.Join(fmt.Errorf("browser download %s size does not match metadata", id), file.Close())
	}
	return entry, file, nil
}
