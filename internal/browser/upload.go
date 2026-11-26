package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
)

var (
	ErrUploadInput   = errors.New("需要有效的浏览器产物标识、CSS 选择器和超时")
	ErrUploadTimeout = errors.New("等待文件输入控件超时")
	ErrUploadFailed  = errors.New("无法向指定元素提供文件")
	ErrUploadRuntime = errors.New("浏览器文件命令失败")
)

type ArtifactUpload struct {
	Kind            string
	SourceSessionID uuid.UUID
	ArtifactID      uuid.UUID
	Selector        string
	Timeout         int
}

func (input ArtifactUpload) valid() bool {
	return (input.Kind == "screenshot" || input.Kind == "download") &&
		input.SourceSessionID != uuid.Nil && input.ArtifactID != uuid.Nil &&
		validClick(input.Selector, input.Timeout)
}

func (s *Service) AttachArtifact(ctx context.Context, projectID, sessionID uuid.UUID, input ArtifactUpload) error {
	session, err := s.Session(ctx, projectID, sessionID)
	if err != nil {
		return err
	}
	if !input.valid() {
		return ErrUploadInput
	}
	var file *os.File
	var filename, mediaType string
	var size int64
	switch input.Kind {
	case "screenshot":
		entry, opened, err := s.OpenScreenshot(ctx, projectID, input.SourceSessionID, input.ArtifactID)
		if err != nil {
			return err
		}
		file, filename, mediaType, size = opened, "screenshot.png", "image/png", entry.SizeBytes
	case "download":
		entry, opened, err := s.OpenDownload(ctx, projectID, input.SourceSessionID, input.ArtifactID)
		if err != nil {
			return err
		}
		file, filename, mediaType, size = opened, downloadFilename(entry.Filename), "application/octet-stream", entry.SizeBytes
	}
	content, readErr := io.ReadAll(io.LimitReader(file, maxDownloadBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return fmt.Errorf("read browser upload artifact: %w", err)
	}
	if int64(len(content)) != size || len(content) > maxDownloadBytes {
		return fmt.Errorf("%w: browser artifact size differs from metadata or exceeds 8 MiB", ErrUploadRuntime)
	}
	return s.runPageAction(ctx, session, "/upload", struct {
		Selector string `json:"selector"`
		Timeout  int    `json:"timeout_ms"`
		Filename string `json:"filename"`
		MIMEType string `json:"mime_type"`
		Content  []byte `json:"content"`
	}{input.Selector, input.Timeout, filename, mediaType, content}, input.Timeout, pageActionErrors{
		ErrUploadInput, ErrUploadTimeout, ErrUploadFailed, ErrUploadRuntime,
	})
}
