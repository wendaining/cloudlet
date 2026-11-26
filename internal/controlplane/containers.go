package controlplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/containerpublish"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
	"github.com/wendaining/cloudlet/internal/operations"
	"gorm.io/gorm"
)

func (h *handler) CreateContainer(c *gin.Context, projectID string, params apigen.CreateContainerParams) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" && membership.Role != "developer" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者和开发者可以创建容器"})
		return
	}
	if h.containers == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "容器执行环境未配置"})
		return
	}
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "容器创建请求需要 application/json"})
		return
	}
	var request containerpublish.Request
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, containerpublish.MaxRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "容器创建请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "容器创建请求需要单个 JSON 对象"})
		return
	}
	result, created, err := h.containers.Create(c.Request.Context(), uuid.MustParse(normalizedID), params.IdempotencyKey, request)
	if result.Container.ID != uuid.Nil {
		c.Header("Location", containerLocation(normalizedID, result.Container.ID))
	}
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrInvalid):
			c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
		case errors.Is(err, database.ErrContainerAlreadyActive),
			errors.Is(err, database.ErrContainerPublicationPending),
			errors.Is(err, database.ErrRouteConflict),
			errors.Is(err, operations.ErrIdempotencyConflict),
			errors.Is(err, containerpublish.ErrRecordedFailure),
			errors.Is(err, containerpublish.ErrPublicationIncomplete),
			errors.Is(err, capacity.ErrIdempotencyConflict),
			errors.Is(err, capacity.ErrOwnershipConflict),
			errors.Is(err, capacity.ErrExecutionReleased),
			errors.Is(err, executions.ErrExecutionConflict):
			c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
		case errors.Is(err, capacity.ErrInsufficientCapacity):
			if result.Container.ID == uuid.Nil {
				h.failure(c, err)
				return
			}
			c.JSON(http.StatusTooManyRequests, containerResponse(result))
		case errors.Is(err, containers.ErrImagePull),
			errors.Is(err, containers.ErrImageDigestMismatch),
			errors.Is(err, containers.ErrNotReady),
			errors.Is(err, containerpublish.ErrRuntimeNotReady),
			errors.Is(err, containers.ErrConflict),
			errors.Is(err, containers.ErrNetworkNotFound):
			if result.Container.ID == uuid.Nil {
				h.failure(c, err)
				return
			}
			c.JSON(http.StatusUnprocessableEntity, containerResponse(result))
		default:
			h.logger.Error("container creation failed", "project", normalizedID, "operation", result.Operation.ID, "error", err)
			h.failure(c, err)
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, containerResponse(result))
}

func (h *handler) GetContainer(c *gin.Context, projectID, containerID string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if h.containers == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "容器执行环境未配置"})
		return
	}
	id, err := uuid.Parse(containerID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "容器不存在"})
		return
	}
	result, err := h.containers.Get(c.Request.Context(), uuid.MustParse(normalizedID), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "容器不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, containerResponse(result))
}

func containerLocation(projectID string, id uuid.UUID) string {
	return fmt.Sprintf("/api/v1/projects/%s/containers/%s", projectID, id)
}

func containerResponse(result containerpublish.Result) apigen.Container {
	container, execution, op := result.Container, result.Execution, result.Operation
	response := apigen.Container{
		Id: container.ID, ProjectId: container.ProjectID, ExecutionId: execution.ID, OperationId: execution.OperationID,
		Name: container.Name, Image: execution.Image, ImageDigest: execution.ImageDigest, Port: int(execution.Port),
		Hostname: execution.Hostname, PathPrefix: execution.PathPrefix, CpuMillis: execution.CPUMillis,
		MemoryBytes: execution.MemoryBytes, Pids: execution.PIDs, OpenFiles: execution.OpenFiles,
		User: execution.UserName, HealthCheckPath: execution.HealthPath, HealthCheckTimeoutMs: execution.HealthTimeoutMS,
		State: container.State, Failure: container.Failure, ExecutionState: execution.State,
		DockerContainerId: execution.DockerContainerID, Target: execution.Target,
		OperationPhase: string(op.Phase), OperationOutcome: string(op.Outcome),
		Generation: int64(execution.Generation), CreatedAt: container.CreatedAt,
	}
	if len(execution.Command) > 0 {
		command := append([]string(nil), execution.Command...)
		response.Command = &command
	}
	if len(execution.Env) > 0 {
		env := append([]string(nil), execution.Env...)
		response.Env = &env
	}
	return response
}
