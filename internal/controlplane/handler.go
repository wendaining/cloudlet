package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/version"
	"github.com/wendaining/cloudlet/migrations"
	"gorm.io/gorm"
)

type handler struct {
	store    *database.Store
	sessions *scs.SessionManager
	config   config.Config
	logger   *slog.Logger
}

func NewHandler(store *database.Store, cfg config.Config, logger *slog.Logger) (http.Handler, func()) {
	h := &handler{store: store, config: cfg, logger: logger}
	router := gin.New()
	router.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	router.GET("/readyz", func(c *gin.Context) {
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
		response = append(response, apigen.Operation{
			Id:        operation.ID,
			Action:    operation.Action,
			Phase:     string(operation.Phase),
			Outcome:   string(operation.Outcome),
			CreatedAt: operation.CreatedAt,
			UpdatedAt: operation.UpdatedAt,
		})
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
