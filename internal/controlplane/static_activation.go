package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"gorm.io/gorm"
)

func (h *handler) GetStaticSitePublication(c *gin.Context, projectID, siteName string) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok || !h.staticSiteActivationAvailable(c) {
		return
	}
	port, ok := h.staticSiteGatewayPort(c)
	if !ok {
		return
	}
	publication, err := h.store.StaticSitePublication(c.Request.Context(), projectID, siteName)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点尚未发布或不存在"})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, activePublicationResponse(publication, port))
	}
}

func (h *handler) ActivateStaticSiteVersion(c *gin.Context, projectID, siteName, versionID string) {
	_, projectID, member, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if member.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以切换站点版本"})
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
	revision, err := decodeActivationRevision(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	if err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "站点切换请求必须包含有效的修订值"})
		return
	}
	port, ok := h.staticSiteGatewayPort(c)
	if !ok {
		return
	}
	publication, err := h.sites.SwitchVersion(c.Request.Context(), h.siteRoutes, projectID, siteName, id, revision)
	var syncErr *routes.SyncError
	switch {
	case errors.As(err, &syncErr):
		h.logger.ErrorContext(c.Request.Context(), "apply static site version switch", "error", err)
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "站点路由尚未生效，请重试同一请求"})
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, staticdeploy.ErrInvalidSiteName):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "站点尚未发布或目标版本不存在"})
	case errors.Is(err, database.ErrStaticSiteRevisionConflict):
		c.JSON(http.StatusConflict, apigen.Error{Message: "发布修订值已变化，请重新查询当前发布"})
	case errors.Is(err, routes.ErrStaticSiteNotReady):
		c.JSON(http.StatusConflict, apigen.Error{Message: "站点版本文件尚未就绪"})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, activePublicationResponse(publication, port))
	}
}

func decodeActivationRevision(reader io.Reader) (time.Time, error) {
	var request apigen.StaticSiteActivationRequest
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return time.Time{}, fmt.Errorf("decode static site activation request: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return time.Time{}, fmt.Errorf("decode trailing activation request data: %w", err)
		}
		return time.Time{}, errors.New("unexpected trailing activation request data")
	}
	if request.ExpectedRevision.IsZero() {
		return time.Time{}, errors.New("static site activation revision is required")
	}
	return request.ExpectedRevision, nil
}

func (h *handler) staticSiteActivationAvailable(c *gin.Context) bool {
	if !h.siteStorageAvailable(c) {
		return false
	}
	if h.siteRoutes == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "站点发布未配置"})
		return false
	}
	return true
}

func (h *handler) staticSiteGatewayPort(c *gin.Context) (string, bool) {
	_, port, err := net.SplitHostPort(h.config.GatewayAddress)
	if err != nil {
		h.failure(c, fmt.Errorf("parse Gateway address for site publication: %w", err))
		return "", false
	}
	return port, true
}

func activePublicationResponse(publication database.StaticSitePublication, port string) apigen.StaticSiteActivePublication {
	return apigen.StaticSiteActivePublication{
		SiteId:    publication.SiteID,
		VersionId: publication.VersionID,
		Hostname:  publication.Host,
		Url:       staticSiteGatewayURL(publication.Host, port),
		Revision:  publication.Revision.UTC(),
	}
}
