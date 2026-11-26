package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/database"
)

const (
	maxScreenshotBytes  = 2 << 20
	maxScreenshotPixels = 4_194_304
)

var (
	ErrScreenshotConflict = errors.New("当前页面无法截图或有浏览器命令正在运行")
	ErrScreenshotTooLarge = errors.New("浏览器截图超过像素或文件大小上限")
	ErrScreenshotTimeout  = errors.New("浏览器截图超时")
	ErrScreenshot         = errors.New("浏览器截图失败")
)

func openBrowserArtifactStore(directory string, maxBytes int64) (*artifacts.Store, error) {
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("configure browser artifact storage: absolute directory required: %w", fs.ErrInvalid)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create browser artifact storage: %w", err)
	}
	probe, err := os.CreateTemp(directory, ".write-check-")
	if err != nil {
		return nil, fmt.Errorf("check browser artifact storage is writable: %w", err)
	}
	if err := errors.Join(probe.Close(), os.Remove(probe.Name())); err != nil {
		return nil, fmt.Errorf("clean browser artifact storage check: %w", err)
	}
	parent, err := os.Open(filepath.Dir(directory))
	if err != nil {
		return nil, fmt.Errorf("open browser artifact parent directory: %w", err)
	}
	if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
		return nil, fmt.Errorf("persist browser artifact directory: %w", err)
	}
	return artifacts.New(directory, maxBytes)
}

func (s *Service) CaptureScreenshot(ctx context.Context, projectID, sessionID uuid.UUID, fullPage bool) (database.BrowserScreenshot, error) {
	session, err := s.Session(ctx, projectID, sessionID)
	if err != nil {
		return database.BrowserScreenshot{}, err
	}
	endpoint, err := s.runtimeEndpoint(ctx, session)
	if err != nil {
		return database.BrowserScreenshot{}, err
	}
	image, err := s.capture(ctx, endpoint, sessionID, fullPage)
	if err != nil {
		return database.BrowserScreenshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return database.BrowserScreenshot{}, err
	}
	digest, err := s.screenshots.Put(bytes.NewReader(image))
	if err != nil {
		return database.BrowserScreenshot{}, fmt.Errorf("persist browser artifact: %w", err)
	}
	return s.store.RegisterBrowserScreenshot(ctx, sessionID, digest, int64(len(image)))
}

func (s *Service) capture(ctx context.Context, endpoint string, id uuid.UUID, fullPage bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	var body io.Reader
	if fullPage {
		body = bytes.NewBufferString(`{"full_page":true}`)
	}
	response, err := s.postRuntime(ctx, endpoint+"/screenshot", id, body)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrScreenshotTimeout
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScreenshot, err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || mediaType != "image/png" {
			return nil, fmt.Errorf("%w: runtime returned a non-PNG response", ErrScreenshot)
		}
		if response.ContentLength > maxScreenshotBytes {
			return nil, ErrScreenshotTooLarge
		}
		return readScreenshot(response.Body)
	case http.StatusConflict:
		return nil, ErrScreenshotConflict
	case http.StatusGone:
		return nil, ErrSessionUnavailable
	case http.StatusUnprocessableEntity:
		return nil, ErrScreenshotTooLarge
	case http.StatusGatewayTimeout:
		return nil, ErrScreenshotTimeout
	default:
		return nil, fmt.Errorf("%w: runtime HTTP %d", ErrScreenshot, response.StatusCode)
	}
}

func readScreenshot(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxScreenshotBytes+1))
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrScreenshotTimeout
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read PNG: %w", ErrScreenshot, err)
	}
	if len(data) > maxScreenshotBytes {
		return nil, ErrScreenshotTooLarge
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: decode PNG header: %w", ErrScreenshot, err)
	}
	if config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: invalid PNG dimensions", ErrScreenshot)
	}
	if config.Height > maxScreenshotPixels/config.Width {
		return nil, ErrScreenshotTooLarge
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return nil, fmt.Errorf("%w: decode PNG: %w", ErrScreenshot, err)
	}
	return data, nil
}

// OpenScreenshot authorizes the stored artifact through its project and session.
// The screenshot can be downloaded after the associated container exits.
func (s *Service) OpenScreenshot(ctx context.Context, projectID, sessionID, screenshotID uuid.UUID) (database.BrowserScreenshot, *os.File, error) {
	screenshot, err := s.store.BrowserScreenshot(ctx, projectID, sessionID, screenshotID, s.hostID)
	if err != nil {
		return database.BrowserScreenshot{}, nil, err
	}
	file, err := s.screenshots.Open(screenshot.Digest)
	if err != nil {
		return database.BrowserScreenshot{}, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return database.BrowserScreenshot{}, nil, errors.Join(err, file.Close())
	}
	if info.Size() != screenshot.SizeBytes {
		return database.BrowserScreenshot{}, nil, errors.Join(
			fmt.Errorf("screenshot %s size differs from stored metadata", screenshotID), file.Close(),
		)
	}
	return screenshot, file, nil
}
