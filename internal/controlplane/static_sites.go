package controlplane

import (
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/siteartifact"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"gorm.io/gorm"
)

func (h *handler) UploadStaticSiteVersion(c *gin.Context, projectID, siteName string) {
	_, projectID, member, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if member.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以上传站点版本"})
		return
	}
	if !h.siteStorageAvailable(c) {
		return
	}
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/zip" {
		c.JSON(http.StatusUnsupportedMediaType, apigen.Error{Message: "请求正文必须使用 application/zip"})
		return
	}
	if c.Request.ContentLength > staticdeploy.MaxUploadBytes {
		siteUploadTooLarge(c)
		return
	}
	version, err := h.sites.Upload(c.Request.Context(), projectID, siteName, c.Request.Body)
	switch {
	case errors.Is(err, staticdeploy.ErrInvalidSiteName):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "站点名称必须为最多 63 个小写字母、数字或以连字符分隔的名称"})
	case errors.Is(err, artifacts.ErrTooLarge):
		siteUploadTooLarge(c)
	case errors.Is(err, siteartifact.ErrInvalidArchive), errors.Is(err, io.ErrUnexpectedEOF):
		h.logger.Warn("reject static site ZIP", "project", projectID, "site", siteName, "error", err)
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "站点 ZIP 损坏或不符合站点产物要求"})
	case err != nil:
		h.failure(c, err)
	default:
		c.Header("Location", c.Request.URL.EscapedPath()+"/"+version.ID.String())
		c.JSON(http.StatusCreated, staticSiteResponse(version))
	}
}

func (h *handler) GetStaticSiteVersion(c *gin.Context, projectID, siteName, versionID string) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok || !h.siteStorageAvailable(c) {
		return
	}
	id, err := uuid.Parse(versionID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
		return
	}
	version, err := h.sites.Version(c.Request.Context(), projectID, siteName, id)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, staticdeploy.ErrInvalidSiteName):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, staticSiteResponse(version))
	}
}

func (h *handler) siteStorageAvailable(c *gin.Context) bool {
	if h.sites == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "站点存储未配置"})
		return false
	}
	return true
}

func siteUploadTooLarge(c *gin.Context) {
	c.JSON(http.StatusRequestEntityTooLarge, apigen.Error{Message: "站点 ZIP 超过 64 MiB 上传上限"})
}

func staticSiteResponse(version database.StaticSiteVersion) apigen.StaticSiteVersion {
	return apigen.StaticSiteVersion{
		Id:             version.ID,
		SiteId:         version.SiteID,
		ArtifactDigest: version.ArtifactDigest,
		CreatedAt:      version.CreatedAt,
		Status:         apigen.Ready,
	}
}
