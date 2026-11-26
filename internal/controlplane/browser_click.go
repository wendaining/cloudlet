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
)

func (h *handler) ClickBrowserPage(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	var body apigen.ClickBrowserPageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&body); err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrClickInput.Error()})
		return
	}
	timeout := 10000
	if body.TimeoutMs != nil {
		timeout = *body.TimeoutMs
	}
	err := h.browser.Click(c.Request.Context(), uuid.MustParse(project), sessionID, body.Selector, timeout)
	switch {
	case errors.Is(err, browser.ErrClickInput):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrClickTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrClickFailed):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrClickRuntime):
		h.logger.Warn("click browser page", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrClickRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		c.Status(http.StatusNoContent)
	}
}
