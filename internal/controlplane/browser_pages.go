package controlplane

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
)

func (h *handler) OpenBrowserPage(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	page, err := h.browser.OpenPage(c.Request.Context(), uuid.MustParse(project), sessionID)
	if err != nil {
		h.pageFailure(c, err)
		return
	}
	c.Header("Location", c.Request.URL.EscapedPath()+"/"+page.ID.String())
	c.JSON(http.StatusCreated, apigen.BrowserPage{Id: page.ID, Url: page.URL, Title: page.Title})
}

func (h *handler) GetBrowserPage(c *gin.Context, projectID string, sessionID, pageID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	page, err := h.browser.DescribePage(c.Request.Context(), uuid.MustParse(project), sessionID, pageID)
	if err != nil {
		h.pageFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, apigen.BrowserPage{Id: page.ID, Url: page.URL, Title: page.Title})
}

func (h *handler) CloseBrowserPage(c *gin.Context, projectID string, sessionID, pageID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	if err := h.browser.ClosePage(c.Request.Context(), uuid.MustParse(project), sessionID, pageID); err != nil {
		h.pageFailure(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) pageFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, browser.ErrPageTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrPageConflict):
		c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
	case errors.Is(err, browser.ErrPageRuntime):
		h.logger.Warn("manage browser page", "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrPageRuntime.Error()})
	default:
		h.browserFailure(c, err)
	}
}
