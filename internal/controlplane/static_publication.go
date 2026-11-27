package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"gorm.io/gorm"
)

func (h *handler) PublishStaticSiteVersion(c *gin.Context, projectID, siteName, versionID string) {
	_, projectID, member, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if member.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以发布站点版本"})
		return
	}
	if !h.staticSiteActivationAvailable(c) {
		return
	}
	id, err := uuid.Parse(versionID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
		return
	}
	var body apigen.StaticSitePublicationRequest
	var trailing json.RawMessage
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "站点发布请求格式无效"})
		return
	}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "站点发布请求格式无效"})
		return
	}
	port, ok := h.staticSiteGatewayPort(c)
	if !ok {
		return
	}
	version, hostname, err := h.sites.Publish(c.Request.Context(), h.siteRoutes, projectID, siteName, id, body.Hostname)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, staticdeploy.ErrInvalidSiteName):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点版本不存在"})
	case errors.Is(err, database.ErrRouteConflict):
		c.JSON(http.StatusConflict, apigen.Error{Message: "域名已被路由占用"})
	case errors.Is(err, database.ErrStaticSiteAlreadyPublished):
		c.JSON(http.StatusConflict, apigen.Error{Message: "站点已发布，不能通过首次发布更换版本或域名"})
	case errors.Is(err, routes.ErrStaticSiteNotReady):
		c.JSON(http.StatusConflict, apigen.Error{Message: "站点版本文件尚未就绪"})
	case err == nil:
		c.JSON(http.StatusOK, apigen.StaticSitePublication{SiteId: version.SiteID, VersionId: version.ID, Hostname: hostname, Url: staticSiteGatewayURL(hostname, port)})
	default:
		if _, syncErr := errors.AsType[*routes.SyncError](err); syncErr {
			h.logger.ErrorContext(c.Request.Context(), "apply static site route", "error", err)
			c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "站点路由尚未生效，请重试同一请求"})
			return
		}
		if errors.Is(err, fs.ErrInvalid) {
			c.JSON(http.StatusBadRequest, apigen.Error{Message: "域名格式无效"})
			return
		}
		h.failure(c, err)
	}
}

func staticSiteGatewayURL(hostname, port string) string {
	host := hostname
	if port != "80" {
		host = net.JoinHostPort(hostname, port)
	}
	publicURL := url.URL{Scheme: "http", Host: host, Path: "/"}
	return publicURL.String()
}
