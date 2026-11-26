package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"gorm.io/gorm"
)

func (h *handler) CaptureBrowserDownload(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	var input apigen.CaptureBrowserDownloadRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&input); err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrDownloadInput.Error()})
		return
	}
	timeout := 10000
	if input.TimeoutMs != nil {
		timeout = *input.TimeoutMs
	}
	entry, err := h.browser.CaptureDownload(c.Request.Context(), uuid.MustParse(project), sessionID, input.Selector, timeout)
	switch {
	case errors.Is(err, browser.ErrDownloadInput):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrDownloadRejected):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrDownloadTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrDownloadRuntime):
		h.logger.Warn("capture browser download", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrDownloadRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		endpoint := "/api/v1/projects/" + project + "/browser-sessions/" + sessionID.String() + "/downloads/" + entry.ID.String()
		c.Header("Location", endpoint)
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, apigen.BrowserDownload{
			Id: entry.ID, Filename: entry.Filename, ArtifactDigest: entry.ArtifactDigest,
			SizeBytes: entry.SizeBytes, DownloadUrl: endpoint, CreatedAt: entry.CreatedAt,
		})
	}
}

func (h *handler) GetBrowserDownload(c *gin.Context, projectID string, sessionID, downloadID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	entry, file, err := h.browser.OpenDownload(c.Request.Context(), uuid.MustParse(project), sessionID, downloadID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "浏览器下载产物不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	defer file.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Filename}))
	c.DataFromReader(http.StatusOK, entry.SizeBytes, "application/octet-stream", file, nil)
}
