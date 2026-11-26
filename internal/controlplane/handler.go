package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alexedwards/scs/postgresstore"
	"github.com/alexedwards/scs/v2"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containerpublish"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/mcpserver"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
	"github.com/wendaining/cloudlet/internal/version"
	"github.com/wendaining/cloudlet/internal/workerpublish"
	"github.com/wendaining/cloudlet/migrations"
	"gorm.io/gorm"
)

type handler struct {
	store      *database.Store
	sites      *staticdeploy.Service
	siteRoutes *routes.Publisher
	browser    *browser.Service
	sessions   *scs.SessionManager
	config     config.Config
	logger     *slog.Logger
	workers    *workerpublish.Service
	containers *containerpublish.Service
}

func NewHandler(store *database.Store, cfg config.Config, logger *slog.Logger) (http.Handler, func()) {
	return newHandler(store, nil, nil, cfg, logger, nil, nil, nil, nil)
}

// NewHandlerWithMCP mounts the MCP endpoint on the management API.
func NewHandlerWithMCP(store *database.Store, sites *staticdeploy.Service, browserService *browser.Service, cfg config.Config, logger *slog.Logger, apiEndpoint string) (http.Handler, func(), error) {
	return NewHandlerWithMCPAndWorkers(store, sites, browserService, cfg, logger, apiEndpoint, nil, nil)
}

// NewHandlerWithMCPAndWorkers mounts the management API with Worker and site publication.
func NewHandlerWithMCPAndWorkers(store *database.Store, sites *staticdeploy.Service, browserService *browser.Service, cfg config.Config, logger *slog.Logger, apiEndpoint string, publication *workerpublish.Service, siteRoutes *routes.Publisher) (http.Handler, func(), error) {
	return NewHandlerWithMCPWorkersAndContainers(store, sites, browserService, cfg, logger, apiEndpoint, publication, siteRoutes, nil)
}

// NewHandlerWithMCPWorkersAndContainers mounts the management API with Worker
// publication, static site publication and container creation.
func NewHandlerWithMCPWorkersAndContainers(store *database.Store, sites *staticdeploy.Service, browserService *browser.Service, cfg config.Config, logger *slog.Logger, apiEndpoint string, publication *workerpublish.Service, siteRoutes *routes.Publisher, containers *containerpublish.Service) (http.Handler, func(), error) {
	mcpHandler, err := mcpserver.NewHandler(apiEndpoint)
	if err != nil {
		return nil, nil, err
	}
	handler, cleanup := newHandler(store, sites, browserService, cfg, logger, mcpHandler, publication, siteRoutes, containers)
	return handler, cleanup, nil
}

// NewHandlerWithWorkers mounts the shared Worker publication API. The caller
// owns publication and must close it after the HTTP handler stops.
func NewHandlerWithWorkers(store *database.Store, cfg config.Config, logger *slog.Logger, publication *workerpublish.Service) (http.Handler, func()) {
	return newHandler(store, nil, nil, cfg, logger, nil, publication, nil, nil)
}

// NewHandlerWithContainers mounts the container creation API. The caller owns
// containers and must close it after the HTTP handler stops.
func NewHandlerWithContainers(store *database.Store, cfg config.Config, logger *slog.Logger, containers *containerpublish.Service) (http.Handler, func()) {
	return newHandler(store, nil, nil, cfg, logger, nil, nil, nil, containers)
}

func newHandler(store *database.Store, sites *staticdeploy.Service, browserService *browser.Service, cfg config.Config, logger *slog.Logger, mcpHandler http.Handler, publication *workerpublish.Service, siteRoutes *routes.Publisher, containers *containerpublish.Service) (http.Handler, func()) {
	h := &handler{store: store, sites: sites, siteRoutes: siteRoutes, browser: browserService, config: cfg, logger: logger, workers: publication, containers: containers}
	router := gin.New()
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/readyz", func(c *gin.Context) {
		if h.workers != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
			defer cancel()
			if err := h.workers.Ready(ctx); err != nil {
				h.logger.Error("Worker controller readiness failed", "error", err)
				c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "Worker 控制器尚未就绪"})
				return
			}
		}
		if h.containers != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
			defer cancel()
			if err := h.containers.Ready(ctx); err != nil {
				h.logger.Error("container controller readiness failed", "error", err)
				c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "容器控制器尚未就绪"})
				return
			}
		}
		if store != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
			defer cancel()
			if err := store.SQL.PingContext(ctx); err != nil {
				h.logger.Error("database readiness failed", "error", err)
				c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "数据库尚未就绪"})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})
	apigen.RegisterHandlers(router, h)
	if store == nil {
		return router, func() {}
	}
	if mcpHandler != nil {
		router.Any("/mcp", gin.WrapH(mcpHandler))
	}
	sessionStore := postgresstore.New(store.SQL)
	h.sessions = scs.New()
	h.sessions.Store = sessionStore
	h.sessions.HashTokenInStore = true
	h.sessions.Lifetime = 24 * time.Hour
	h.sessions.IdleTimeout = time.Hour
	h.sessions.Cookie.Name = "cloudlet_" + cfg.Instance + "_session"
	h.sessions.Cookie.HttpOnly = true
	h.sessions.Cookie.SameSite = http.SameSiteLaxMode
	h.sessions.Cookie.Secure = strings.HasPrefix(cfg.PublicOrigin, "https://")
	h.sessions.ErrorFunc = func(w http.ResponseWriter, _ *http.Request, err error) {
		logger.Error("session storage failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(apigen.Error{Message: "会话存储失败"})
	}
	return http.NewCrossOriginProtection().Handler(h.sessions.LoadAndSave(router)), sessionStore.StopCleanup
}

func (h *handler) GetVersion(c *gin.Context) { c.JSON(http.StatusOK, version.Current()) }

func (h *handler) Login(c *gin.Context) {
	if !h.databaseAvailable(c) {
		return
	}
	var body apigen.LoginRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "登录请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || body.Email == "" || body.Password == "" || len(body.Password) > 72 {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "登录请求格式无效"})
		return
	}
	user, err := h.store.Authenticate(c.Request.Context(), body.Email, body.Password)
	if errors.Is(err, database.ErrCredentials) {
		c.JSON(http.StatusUnauthorized, apigen.Error{Message: err.Error()})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	if err := h.sessions.RenewToken(c.Request.Context()); err != nil {
		h.failure(c, err)
		return
	}
	h.sessions.Put(c.Request.Context(), "user_id", user.ID)
	projects, err := h.store.Projects(c.Request.Context(), user.ID)
	if err != nil {
		h.failure(c, err)
		return
	}
	response := make([]apigen.Project, 0, len(projects))
	for _, project := range projects {
		response = append(response, apigen.Project{Id: uuid.MustParse(project.ID), Name: project.Name})
	}
	c.JSON(http.StatusOK, apigen.Session{User: apigen.User{Id: user.ID, Email: user.Email}, Projects: response})
}

func (h *handler) Logout(c *gin.Context) {
	if !h.databaseAvailable(c) {
		return
	}
	if err := h.sessions.Destroy(c.Request.Context()); err != nil {
		h.failure(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) GetSession(c *gin.Context) {
	user, ok := h.currentUser(c)
	if ok {
		projects, err := h.store.Projects(c.Request.Context(), user.ID)
		if err != nil {
			h.failure(c, err)
			return
		}
		response := make([]apigen.Project, 0, len(projects))
		for _, project := range projects {
			response = append(response, apigen.Project{Id: uuid.MustParse(project.ID), Name: project.Name})
		}
		c.JSON(http.StatusOK, apigen.Session{User: apigen.User{Id: user.ID, Email: user.Email}, Projects: response})
	}
}

func (h *handler) GetEnvironment(c *gin.Context) {
	if _, ok := h.currentUser(c); !ok {
		return
	}
	c.JSON(http.StatusOK, apigen.Environment{Instance: h.config.Instance, DatabaseVersion: migrations.Version})
}

func (h *handler) CreateProject(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	var body apigen.CreateProjectRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目请求格式无效"})
		return
	}
	var trailing any
	name, validName := normalizeProjectName(body.Name)
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || !validName {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目名称需要 1 至 100 个非空字符"})
		return
	}
	project, err := h.store.CreateProject(c.Request.Context(), user.ID, name)
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusCreated, projectResponse(project))
}

func (h *handler) ListProjects(c *gin.Context) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	projects, err := h.store.Projects(c.Request.Context(), user.ID)
	if err != nil {
		h.failure(c, err)
		return
	}
	response := make([]apigen.Project, 0, len(projects))
	for _, project := range projects {
		response = append(response, projectResponse(project))
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) GetProject(c *gin.Context, projectID string) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	parsedID, err := uuid.Parse(projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return
	}
	project, err := h.store.Project(c.Request.Context(), user.ID, parsedID.String())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, projectResponse(project))
}

func (h *handler) RenameProject(c *gin.Context, projectID string) {
	user, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以重命名项目"})
		return
	}
	var body apigen.RenameProjectRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目名称请求格式无效"})
		return
	}
	var trailing any
	name, validName := normalizeProjectName(body.Name)
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || !validName {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目名称需要 1 至 100 个非空字符"})
		return
	}
	if err := h.store.RenameProject(c.Request.Context(), normalizedID, user.ID, name); err != nil {
		if errors.Is(err, database.ErrProjectNotOwner) {
			c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以重命名项目"})
			return
		}
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, projectResponse(database.Project{ID: normalizedID, Name: name}))
}

// GetProjectCapacity exposes the project quota and active usage. Host budgets
// remain internal because they aggregate usage across projects.
func (h *handler) GetProjectCapacity(c *gin.Context, projectID string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	capacityStore, err := capacity.NewStore(h.store.DB)
	if err != nil {
		h.failure(c, err)
		return
	}
	usage, err := capacityStore.ProjectUsage(c.Request.Context(), uuid.MustParse(normalizedID))
	if errors.Is(err, capacity.ErrProjectQuotaNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目配额未配置"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, projectCapacityResponse(usage))
}

// UpdateProjectCapacity adjusts the project quota for an owner. The capacity
// store holds the quota row lock while checking active reservations, so a
// limit below current usage is rejected without changing the quota or any
// running execution.
func (h *handler) UpdateProjectCapacity(c *gin.Context, projectID string) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以调整项目额度"})
		return
	}
	var body apigen.UpdateProjectCapacityRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目额度请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目额度请求格式无效"})
		return
	}
	resources := capacity.Resources{
		CPUMillis:   body.CpuMillis,
		MemoryBytes: body.MemoryBytes,
		Instances:   body.Instances,
	}
	if resources.CPUMillis <= 0 || resources.MemoryBytes <= 0 || resources.Instances <= 0 {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "项目额度需要正的 CPU 毫核、内存字节与实例数"})
		return
	}
	capacityStore, err := capacity.NewStore(h.store.DB)
	if err != nil {
		h.failure(c, err)
		return
	}
	usage, err := capacityStore.UpdateProjectQuota(c.Request.Context(), uuid.MustParse(normalizedID), resources)
	if errors.Is(err, capacity.ErrQuotaInUse) {
		c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
		return
	}
	if errors.Is(err, capacity.ErrProjectNotFound) || errors.Is(err, capacity.ErrProjectQuotaNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return
	}
	if errors.Is(err, fs.ErrInvalid) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, projectCapacityResponse(usage))
}

func (h *handler) ListProjectMembers(c *gin.Context, projectID string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	members, err := h.store.ProjectMembers(c.Request.Context(), normalizedID)
	if err != nil {
		h.failure(c, err)
		return
	}
	response := make([]apigen.ProjectMember, 0, len(members))
	for _, member := range members {
		response = append(response, apigen.ProjectMember{
			UserId: member.UserID,
			Email:  member.Email,
			Role:   apigen.ProjectRole(member.Role),
		})
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) UpsertProjectMember(c *gin.Context, projectID string) {
	user, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以管理成员"})
		return
	}
	var body apigen.UpsertProjectMemberRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "成员请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "成员请求格式无效"})
		return
	}
	email, err := database.NormalizeEmail(body.Email)
	if err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "成员邮箱格式无效"})
		return
	}
	role := string(body.Role)
	if role != "developer" && role != "viewer" {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "成员角色必须是 developer 或 viewer"})
		return
	}
	target, err := h.store.UserByEmail(c.Request.Context(), email)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "账号不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	if target.ID == user.ID {
		c.JSON(http.StatusConflict, apigen.Error{Message: "不能修改项目所有者"})
		return
	}
	existing, err := h.store.ProjectMember(c.Request.Context(), normalizedID, target.ID)
	if err == nil && existing.Role == "owner" {
		c.JSON(http.StatusConflict, apigen.Error{Message: "不能修改项目所有者"})
		return
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		h.failure(c, err)
		return
	}
	if err := h.store.UpsertProjectMember(c.Request.Context(), normalizedID, target.ID, role); err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, apigen.ProjectMember{
		UserId: target.ID,
		Email:  target.Email,
		Role:   apigen.ProjectRole(role),
	})
}

func (h *handler) RemoveProjectMember(c *gin.Context, projectID, userID string) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以管理成员"})
		return
	}
	targetID, err := uuid.Parse(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目成员不存在"})
		return
	}
	err = h.store.RemoveProjectMember(c.Request.Context(), normalizedID, targetID.String())
	if errors.Is(err, database.ErrProjectOwner) {
		c.JSON(http.StatusConflict, apigen.Error{Message: "不能移除项目所有者"})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目成员不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *handler) CreateProjectSecretVersion(c *gin.Context, projectID string) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以管理项目凭据"})
		return
	}
	var body apigen.CreateProjectSecretRequest
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 6*database.MaxSecretValueBytes+4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "凭据请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "凭据请求格式无效"})
		return
	}
	version, err := h.store.CreateProjectSecretVersion(c.Request.Context(), normalizedID, body.Name, body.Value, h.config.SecretKey)
	if errors.Is(err, fs.ErrInvalid) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusCreated, projectSecretVersionResponse(version))
}

func (h *handler) ListProjectSecrets(c *gin.Context, projectID string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	secrets, err := h.store.ProjectSecrets(c.Request.Context(), normalizedID)
	if err != nil {
		h.failure(c, err)
		return
	}
	response := make([]apigen.ProjectSecret, 0, len(secrets))
	for _, secret := range secrets {
		response = append(response, apigen.ProjectSecret{
			Id:            uuid.MustParse(secret.ID),
			Name:          secret.Name,
			LatestVersion: secret.LatestVersion,
			CreatedAt:     secret.CreatedAt,
			UpdatedAt:     secret.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) ListProjectSecretVersions(c *gin.Context, projectID, name string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	versions, err := h.store.ProjectSecretVersions(c.Request.Context(), normalizedID, name)
	if errors.Is(err, fs.ErrInvalid) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "凭据不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	if len(versions) == 0 {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "凭据不存在"})
		return
	}
	response := make([]apigen.ProjectSecretVersion, 0, len(versions))
	for _, version := range versions {
		response = append(response, projectSecretVersionResponse(version))
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) GetProjectSecretValue(c *gin.Context, projectID, name string, version int) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以读取项目凭据"})
		return
	}
	value, err := h.store.ProjectSecretValueByName(c.Request.Context(), normalizedID, name, version, h.config.SecretKey)
	if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, fs.ErrInvalid) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "凭据版本不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, apigen.ProjectSecretValue{
		Id:        uuid.MustParse(value.ID),
		SecretId:  uuid.MustParse(value.SecretID),
		Name:      value.Name,
		Version:   value.Version,
		CreatedAt: value.CreatedAt,
		Value:     value.Value,
	})
}

func (h *handler) ListProjectOperations(c *gin.Context, projectID string) {
	user, ok := h.currentUser(c)
	if !ok {
		return
	}
	parsedID, err := uuid.Parse(projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return
	}
	// Resolve project access first so an invisible project is a 404 rather than an empty list.
	if _, err := h.store.Project(c.Request.Context(), user.ID, parsedID.String()); errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return
	} else if err != nil {
		h.failure(c, err)
		return
	}
	operations, err := h.store.ProjectOperations(c.Request.Context(), user.ID, parsedID.String())
	if err != nil {
		h.failure(c, err)
		return
	}
	response := make([]apigen.Operation, 0, len(operations))
	for _, operation := range operations {
		response = append(response, operationResponse(operation))
	}
	c.JSON(http.StatusOK, response)
}

func (h *handler) projectAccess(c *gin.Context, projectID string) (database.User, string, database.ProjectMember, bool) {
	user, ok := h.currentUser(c)
	if !ok {
		return database.User{}, "", database.ProjectMember{}, false
	}
	parsedID, err := uuid.Parse(projectID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return database.User{}, "", database.ProjectMember{}, false
	}
	member, err := h.store.ProjectMember(c.Request.Context(), parsedID.String(), user.ID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目不存在或没有访问权限"})
		return database.User{}, "", database.ProjectMember{}, false
	}
	if err != nil {
		h.failure(c, err)
		return database.User{}, "", database.ProjectMember{}, false
	}
	return user, parsedID.String(), member, true
}

func normalizeProjectName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	return name, name != "" && utf8.RuneCountInString(name) <= 100 && !strings.ContainsRune(name, '\x00')
}

func projectResponse(project database.Project) apigen.Project {
	return apigen.Project{Id: uuid.MustParse(project.ID), Name: project.Name}
}

func resourceAmounts(resources capacity.Resources) apigen.ResourceAmounts {
	return apigen.ResourceAmounts{
		CpuMillis:   resources.CPUMillis,
		MemoryBytes: resources.MemoryBytes,
		Instances:   resources.Instances,
	}
}

func projectCapacityResponse(usage capacity.ProjectUsage) apigen.ProjectCapacity {
	return apigen.ProjectCapacity{
		ProjectId: usage.ProjectID,
		Limit:     resourceAmounts(usage.Limit),
		Used:      resourceAmounts(usage.Used),
		Remaining: resourceAmounts(usage.Remaining),
	}
}

func projectSecretVersionResponse(version database.ProjectSecretVersion) apigen.ProjectSecretVersion {
	return apigen.ProjectSecretVersion{
		Id:        uuid.MustParse(version.ID),
		SecretId:  uuid.MustParse(version.SecretID),
		Name:      version.Name,
		Version:   version.Version,
		CreatedAt: version.CreatedAt,
	}
}

func (h *handler) currentUser(c *gin.Context) (database.User, bool) {
	if !h.databaseAvailable(c) {
		return database.User{}, false
	}
	id := h.sessions.GetString(c.Request.Context(), "user_id")
	if id == "" {
		c.JSON(http.StatusUnauthorized, apigen.Error{Message: "请先登录"})
		return database.User{}, false
	}
	user, err := h.store.User(c.Request.Context(), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusUnauthorized, apigen.Error{Message: "账号不存在"})
		return database.User{}, false
	}
	if err != nil {
		h.failure(c, err)
		return database.User{}, false
	}
	return user, true
}

func (h *handler) databaseAvailable(c *gin.Context) bool {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "管理数据库未配置"})
		return false
	}
	return true
}

func (h *handler) failure(c *gin.Context, err error) {
	h.logger.Error("management request failed", "path", c.Request.URL.Path, "error", err)
	c.JSON(http.StatusInternalServerError, apigen.Error{Message: "管理服务处理请求失败"})
}
