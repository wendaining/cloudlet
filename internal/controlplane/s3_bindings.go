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

func (h *handler) CreateS3Binding(c *gin.Context, projectID string) {
	_, projectID, member, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if member.Role != "owner" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者可以登记 S3 绑定"})
		return
	}
	var input apigen.S3BindingInput
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 4096))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	var trailing any
	if err != nil || !errors.Is(decoder.Decode(&trailing), io.EOF) || !validS3BindingInput(input) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "S3 绑定请求格式无效"})
		return
	}
	if len(h.config.SecretKey) == 0 {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "项目凭据存储未配置"})
		return
	}
	ctx := c.Request.Context()
	_, err = h.store.S3Binding(ctx, projectID, input.Name)
	switch {
	case err == nil:
		c.JSON(http.StatusConflict, apigen.Error{Message: "S3 绑定名称已存在"})
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
	diagnostic, err := verifyS3Binding(ctx, input, secret.Value, h.config.Environment == "development")
	if err != nil {
		h.logger.Info("reject S3 binding", "project", projectID, "binding", input.Name, "diagnostic", diagnostic)
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: diagnostic})
		return
	}
	binding := database.S3Binding{
		ProjectID:       projectID,
		Name:            input.Name,
		Endpoint:        input.Endpoint,
		Region:          input.Region,
		Bucket:          input.Bucket,
		SecretVersionID: input.SecretVersionId,
		VerifiedAt:      time.Now().UTC().Truncate(time.Microsecond),
	}
	created, err := h.store.CreateS3Binding(ctx, binding)
	switch {
	case err != nil:
		h.failure(c, err)
	case !created:
		c.JSON(http.StatusConflict, apigen.Error{Message: "S3 绑定名称已存在"})
	default:
		c.Header("Location", c.Request.URL.EscapedPath()+"/"+binding.Name)
		c.JSON(http.StatusCreated, s3BindingResponse(binding))
	}
}

func (h *handler) GetS3Binding(c *gin.Context, projectID, name string) {
	_, projectID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	binding, err := h.store.S3Binding(c.Request.Context(), projectID, name)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "S3 绑定不存在"})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, s3BindingResponse(binding))
	}
}

func s3BindingResponse(binding database.S3Binding) apigen.S3Binding {
	return apigen.S3Binding{
		Name:            binding.Name,
		Endpoint:        binding.Endpoint,
		Region:          binding.Region,
		Bucket:          binding.Bucket,
		SecretVersionId: binding.SecretVersionID,
		VerifiedAt:      binding.VerifiedAt.UTC(),
	}
}
