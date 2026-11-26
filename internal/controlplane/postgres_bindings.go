package controlplane

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/database"
	"gorm.io/gorm"
)

func (h *handler) CreatePostgresBinding(c *gin.Context, projectID string) {
	_, projectID, member, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if member.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以登记 PostgreSQL 绑定"})
		return
	}
	var input apigen.PostgresBindingInput
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	var trailing any
	if err != nil || !errors.Is(decoder.Decode(&trailing), io.EOF) || !validPostgresBindingInput(input) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "PostgreSQL 绑定请求格式无效"})
		return
	}
	if len(h.config.SecretKey) == 0 {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "项目凭据存储未配置"})
		return
	}
	ctx := c.Request.Context()
	_, err = h.store.PostgresBinding(ctx, projectID, input.Name)
	switch {
	case err == nil:
		c.JSON(http.StatusConflict, apigen.Error{Message: "PostgreSQL 绑定名称已存在"})
		return
	case !errors.Is(err, gorm.ErrRecordNotFound):
		h.failure(c, err)
		return
	}
	secret, err := h.store.ProjectSecretValue(ctx, projectID, input.SecretVersionId.String(), h.config.SecretKey)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "项目凭据版本不存在"})
		return
	case err != nil:
		h.failure(c, err)
		return
	}
	diagnostic, err := verifyPostgresBinding(ctx, input, secret.Value, h.config.Environment == "development")
	if err != nil {
		h.logger.Info("reject PostgreSQL binding", "project", projectID, "binding", input.Name, "diagnostic", diagnostic)
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: diagnostic})
		return
	}
	binding := database.PostgresBinding{
		ProjectID:       projectID,
		Name:            input.Name,
		Host:            input.Host,
		Port:            input.Port,
		Database:        input.Database,
		SecretVersionID: input.SecretVersionId,
		VerifiedAt:      time.Now().UTC().Truncate(time.Microsecond),
	}
	created, err := h.store.CreatePostgresBinding(ctx, binding)
	switch {
	case err != nil:
		h.failure(c, err)
	case !created:
		c.JSON(http.StatusConflict, apigen.Error{Message: "PostgreSQL 绑定名称已存在"})
	default:
		c.Header("Location", c.Request.URL.EscapedPath()+"/"+binding.Name)
		c.JSON(http.StatusCreated, postgresBindingResponse(binding))
	}
}

func (h *handler) GetPostgresBinding(c *gin.Context, projectID, name string) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	binding, err := h.store.PostgresBinding(c.Request.Context(), projectID, name)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "PostgreSQL 绑定不存在"})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, postgresBindingResponse(binding))
	}
}

func postgresBindingResponse(binding database.PostgresBinding) apigen.PostgresBinding {
	return apigen.PostgresBinding{
		Name:            binding.Name,
		Host:            binding.Host,
		Port:            binding.Port,
		Database:        binding.Database,
		SecretVersionId: binding.SecretVersionID,
		VerifiedAt:      binding.VerifiedAt.UTC(),
	}
}
