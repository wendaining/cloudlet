package controlplane

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"gorm.io/gorm"
)

func (h *handler) ListStaticSites(c *gin.Context, projectID string, params apigen.ListStaticSitesParams) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok || !h.siteStorageAvailable(c) {
		return
	}
	page, err := h.sites.ListSites(c.Request.Context(), projectID, params.Limit, params.Cursor)
	if err != nil {
		h.siteListError(c, err)
		return
	}
	items := make([]apigen.StaticSiteSummary, 0, len(page.Items))
	for _, site := range page.Items {
		items = append(items, apigen.StaticSiteSummary{Id: site.ID, Name: site.Name, CreatedAt: site.CreatedAt})
	}
	c.JSON(http.StatusOK, apigen.StaticSitePage{Sites: items, NextCursor: page.NextCursor})
}

func (h *handler) ListStaticSiteVersions(c *gin.Context, projectID, siteName string, params apigen.ListStaticSiteVersionsParams) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok || !h.siteStorageAvailable(c) {
		return
	}
	page, err := h.sites.ListVersions(c.Request.Context(), projectID, siteName, params.Limit, params.Cursor)
	if err != nil {
		h.siteListError(c, err)
		return
	}
	items := make([]apigen.StaticSiteVersion, 0, len(page.Items))
	for _, version := range page.Items {
		items = append(items, staticSiteResponse(version))
	}
	c.JSON(http.StatusOK, apigen.StaticSiteVersionPage{Versions: items, NextCursor: page.NextCursor})
}

func (h *handler) siteListError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, staticdeploy.ErrInvalidPagination):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "分页参数无效"})
	case errors.Is(err, staticdeploy.ErrInvalidSiteName), errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点不存在"})
	default:
		h.failure(c, err)
	}
}
