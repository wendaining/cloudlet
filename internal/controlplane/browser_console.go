package controlplane

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
)

func (h *handler) GetBrowserConsole(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	history, err := h.browser.Console(c.Request.Context(), uuid.MustParse(project), sessionID)
	if errors.Is(err, browser.ErrConsoleUnavailable) {
		h.logger.Warn("read browser console", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrConsoleUnavailable.Error()})
		return
	}
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	entries := make([]apigen.BrowserConsoleEntry, 0, len(history.Entries))
	for _, entry := range history.Entries {
		entries = append(entries, apigen.BrowserConsoleEntry(entry))
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, apigen.BrowserConsole{Entries: entries, Dropped: history.Dropped})
}
