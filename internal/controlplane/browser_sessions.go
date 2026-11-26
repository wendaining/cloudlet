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
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/database"
	"gorm.io/gorm"
)

func (h *handler) CreateBrowserSession(c *gin.Context, projectID string) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	session, err := h.browser.Create(c.Request.Context(), uuid.MustParse(project), h.config.BrowserSessionLifetime)
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	c.Header("Location", browserEndpoint(session))
	c.JSON(http.StatusCreated, browserSessionResponse(session))
}

func (h *handler) ListBrowserSessions(c *gin.Context, projectID string, params apigen.ListBrowserSessionsParams) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	limit := browser.DefaultSessionLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	views, next, err := h.browser.List(c.Request.Context(), uuid.MustParse(project), params.Before, limit)
	if errors.Is(err, browser.ErrSessionListLimit) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "每页会话数必须在 1 至 100 之间"})
		return
	}
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	items := make([]apigen.BrowserSession, 0, len(views))
	for _, view := range views {
		items = append(items, browserSessionViewResponse(view))
	}
	c.JSON(http.StatusOK, apigen.BrowserSessionPage{Items: items, NextCursor: next})
}

func (h *handler) GetBrowserSession(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, false) {
		return
	}
	session, err := h.browser.Session(c.Request.Context(), uuid.MustParse(project), sessionID)
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	view, err := h.browser.Describe(c.Request.Context(), session)
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, browserSessionViewResponse(view))
}

func (h *handler) CloseBrowserSession(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	session, err := h.browser.CloseSession(c.Request.Context(), uuid.MustParse(project), sessionID)
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, browserSessionResponse(session))
}

func (h *handler) NavigateBrowserSession(c *gin.Context, projectID string, sessionID uuid.UUID) {
	_, project, member, ok := h.projectAccess(c, projectID)
	if !ok || !h.browserAccess(c, member, true) {
		return
	}
	var body apigen.NavigateBrowserSessionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "导航请求格式无效"})
		return
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "导航请求格式无效"})
		return
	}
	page, err := h.browser.Navigate(c.Request.Context(), uuid.MustParse(project), sessionID, body.Url)
	if err != nil {
		h.browserFailure(c, err)
		return
	}
	c.JSON(http.StatusOK, apigen.BrowserNavigation{Url: page.URL, Title: page.Title})
}

func (h *handler) browserAccess(c *gin.Context, member database.ProjectMember, write bool) bool {
	if write && member.Role != "owner" && member.Role != "developer" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者和开发者可以操作浏览器会话"})
		return false
	}
	if h.browser == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "Browser Run 未配置"})
		return false
	}
	return true
}

func (h *handler) browserFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "浏览器会话不存在"})
	case errors.Is(err, capacity.ErrInsufficientCapacity):
		c.JSON(http.StatusConflict, apigen.Error{Message: "浏览器会话容量不足"})
	case errors.Is(err, browser.ErrSessionUnavailable):
		c.JSON(http.StatusConflict, apigen.Error{Message: browser.ErrSessionUnavailable.Error()})
	case errors.Is(err, browser.ErrInvalidURL):
		c.JSON(http.StatusBadRequest, apigen.Error{Message: browser.ErrInvalidURL.Error()})
	case errors.Is(err, browser.ErrNavigationTimeout):
		c.JSON(http.StatusGatewayTimeout, apigen.Error{Message: browser.ErrNavigationTimeout.Error()})
	case errors.Is(err, browser.ErrNavigation):
		c.JSON(http.StatusBadGateway, apigen.Error{Message: browser.ErrNavigation.Error()})
	default:
		h.failure(c, err)
	}
}

func browserEndpoint(session database.BrowserSession) string {
	return "/api/v1/projects/" + session.ProjectID.String() + "/browser-sessions/" + session.ID.String()
}

func browserSessionResponse(session database.BrowserSession) apigen.BrowserSession {
	desired := apigen.BrowserSessionStatusReady
	if session.Status == "closed" || session.Status == "failed" {
		desired = apigen.BrowserSessionStatusClosed
	}
	response := apigen.BrowserSession{
		Id: session.ID, Endpoint: browserEndpoint(session), ExpiresAt: session.ExpiresAt,
		Status: apigen.BrowserSessionStatus(session.Status), DesiredStatus: desired,
		ActualStatus: apigen.BrowserSessionStatus(session.Status),
	}
	if session.LastError != "" {
		response.LastError = &session.LastError
	}
	return response
}

func browserSessionViewResponse(view browser.SessionView) apigen.BrowserSession {
	response := browserSessionResponse(view.BrowserSession)
	response.DesiredStatus = apigen.BrowserSessionStatus(view.DesiredStatus)
	response.ActualStatus = apigen.BrowserSessionStatus(view.ActualStatus)
	return response
}
