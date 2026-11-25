package controlplane

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"gorm.io/gorm"
)

func (h *handler) CaptureBrowserScreenshot(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	screenshot, err := h.browser.CaptureScreenshot(c.Request.Context(), uuid.MustParse(project), sessionID)
	switch {
	case errors.Is(err, browser.ErrScreenshotConflict):
		c.JSON(http.StatusConflict, apigen.Error{Message: browser.ErrScreenshotConflict.Error()})
	case errors.Is(err, browser.ErrScreenshotTooLarge):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: browser.ErrScreenshotTooLarge.Error()})
	case errors.Is(err, browser.ErrScreenshotTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: browser.ErrScreenshotTimeout.Error()})
	case errors.Is(err, browser.ErrScreenshot):
		h.logger.Warn("capture browser screenshot", "session", sessionID, "error", err)
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrScreenshot.Error()})
	case err != nil:
		h.browserFailure(c, err)
	default:
		path := "/api/v1/projects/" + project + "/browser-sessions/" + sessionID.String() + "/screenshots/" + screenshot.ID.String()
		c.Header("Location", path)
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusCreated, apigen.BrowserScreenshot{
			Id: screenshot.ID, ArtifactDigest: screenshot.Digest, SizeBytes: screenshot.SizeBytes,
			DownloadUrl: path, CreatedAt: screenshot.CreatedAt,
		})
	}
}

func (h *handler) DownloadBrowserScreenshot(c *gin.Context, projectID string, sessionID, screenshotID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	screenshot, file, err := h.browser.OpenScreenshot(c.Request.Context(), uuid.MustParse(project), sessionID, screenshotID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "浏览器截图不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	defer file.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Disposition", `attachment; filename="screenshot-`+screenshotID.String()+`.png"`)
	c.DataFromReader(http.StatusOK, screenshot.SizeBytes, "image/png", file, nil)
}
