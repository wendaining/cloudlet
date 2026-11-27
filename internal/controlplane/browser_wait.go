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

func (h *handler) WaitForBrowserPage(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	var body apigen.WaitForBrowserPageRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&body); err != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrWaitInput.Error()})
		return
	}
	timeout := 10000
	if body.TimeoutMs != nil {
		timeout = *body.TimeoutMs
	}
	err := h.browser.Wait(c.Request.Context(), uuid.MustParse(project), sessionID, browser.PageWait{
		Condition: string(body.Condition), Text: body.Text, Timeout: timeout,
	})
	switch {
	case errors.Is(err, browser.ErrWaitInput):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrWaitTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrWaitFailed):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrWaitRuntime):
		h.logger.Warn("wait for browser page", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrWaitRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		c.Status(http.StatusNoContent)
	}
}
