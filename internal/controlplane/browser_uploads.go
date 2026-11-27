package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"gorm.io/gorm"
)

func (h *handler) AttachBrowserArtifact(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	var body apigen.AttachBrowserArtifactRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&body); err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrUploadInput.Error()})
		return
	}
	timeout := 10000
	if body.TimeoutMs != nil {
		timeout = *body.TimeoutMs
	}
	err := h.browser.AttachArtifact(c.Request.Context(), uuid.MustParse(project), sessionID, browser.ArtifactUpload{
		Kind: string(body.ArtifactKind), SourceSessionID: body.SourceSessionId,
		ArtifactID: body.ArtifactId, Selector: body.Selector, Timeout: timeout,
	})
	switch {
	case errors.Is(err, browser.ErrUploadInput):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "浏览器会话或产物不存在"})
	case errors.Is(err, browser.ErrUploadTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrUploadFailed):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrUploadRuntime):
		h.logger.Warn("attach browser artifact", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrUploadRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		c.Status(http.StatusNoContent)
	}
}
