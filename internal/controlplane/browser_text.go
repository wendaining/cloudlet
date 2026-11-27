package controlplane

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
)

func (h *handler) GetBrowserPageText(c *gin.Context, projectID string, sessionID uuid.UUID, params apigen.GetBrowserPageTextParams) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	text, err := h.browser.Text(c.Request.Context(), uuid.MustParse(project), sessionID, params.Selector)
	switch {
	case errors.Is(err, browser.ErrTextScope):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrTextScopeMissing):
		c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrTextTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrTextRuntime):
		h.logger.Warn("read browser text", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrTextRuntime.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, apigen.BrowserPageText{
			Title: text.Title, TitleTruncated: text.TitleTruncated, Text: text.Text, TextTruncated: text.TextTruncated,
		})
	}
}
