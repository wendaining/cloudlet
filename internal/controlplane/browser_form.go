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

func (h *handler) ApplyBrowserFormAction(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	var body struct {
		Action    string  `json:"action"`
		Selector  string  `json:"selector"`
		Value     *string `json:"value"`
		TimeoutMs *int    `json:"timeout_ms"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	var extra any
	if err := decoder.Decode(&body); err != nil || body.Value == nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrFormInput.Error()})
		return
	}
	timeout := 10000
	if body.TimeoutMs != nil {
		timeout = *body.TimeoutMs
	}
	err := h.browser.ApplyFormAction(c.Request.Context(), uuid.MustParse(project), sessionID, browser.FormAction{
		Action: body.Action, Selector: body.Selector, Value: *body.Value, Timeout: timeout,
	})
	switch {
	case errors.Is(err, browser.ErrFormInput):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrFormTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrFormFailed):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrFormRuntime):
		h.logger.Warn("apply browser form action", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrFormRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		c.Status(http.StatusNoContent)
	}
}
