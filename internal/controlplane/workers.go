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
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
	"github.com/wendaining/cloudlet/internal/workerpublish"
	"github.com/wendaining/cloudlet/internal/workers"
	"gorm.io/gorm"
)

func (h *handler) DeployWorker(c *gin.Context, projectID, name string, params apigen.DeployWorkerParams) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" && membership.Role != "developer" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者和开发者可以发布 Worker"})
		return
	}
	if h.workers == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "Worker 执行环境未配置"})
		return
	}
	contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || contentType != "application/json" {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "Worker 发布请求需要 application/json"})
		return
	}
	var request workerpublish.Request
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 6*workerpublish.MaxModuleBytes+(64<<10)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "Worker 发布请求格式无效"})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, apigen.Error{Message: "Worker 发布请求需要单个 JSON 对象"})
		return
	}
	result, created, err := h.workers.Deploy(c.Request.Context(), uuid.MustParse(normalizedID), name, params.IdempotencyKey, request)
	if result.Deployment.ID != uuid.Nil {
		c.Header("Location", deploymentLocation(normalizedID, result.Deployment.ID))
	}
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrInvalid):
			c.JSON(http.StatusBadRequest, apigen.Error{Message: err.Error()})
		case errors.Is(err, database.ErrWorkerAlreadyActive), errors.Is(err, database.ErrWorkerPublicationPending), errors.Is(err, database.ErrWorkerHostnameChanged), errors.Is(err, database.ErrWorkerDeploymentNotReady), errors.Is(err, database.ErrRouteConflict), errors.Is(err, operations.ErrIdempotencyConflict), errors.Is(err, workerpublish.ErrPublicationIncomplete):
			c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
		default:
			h.logger.Error("Worker publication failed", "project", normalizedID, "operation", result.Operation.ID, "error", err)
			status := http.StatusInternalServerError
			var preflight *workers.PreflightError
			if errors.As(err, &preflight) || errors.Is(err, workerpublish.ErrRuntimeNotReady) {
				status = http.StatusUnprocessableEntity
			}
			if errors.Is(err, capacity.ErrInsufficientCapacity) {
				status = http.StatusTooManyRequests
			}
			if result.Deployment.ID == uuid.Nil || status == http.StatusInternalServerError {
				h.failure(c, err)
				return
			}
			c.JSON(status, workerDeploymentResponse(result))
		}
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	c.JSON(status, workerDeploymentResponse(result))
}

func (h *handler) GetWorkerDeployment(c *gin.Context, projectID, deploymentID string) {
	_, normalizedID, _, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if h.workers == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "Worker 执行环境未配置"})
		return
	}
	id, err := uuid.Parse(deploymentID)
	if err != nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "Worker 版本不存在"})
		return
	}
	result, err := h.workers.Get(c.Request.Context(), uuid.MustParse(normalizedID), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "Worker 版本不存在"})
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	c.JSON(http.StatusOK, workerDeploymentResponse(result))
}

func (h *handler) ActivateWorkerDeployment(c *gin.Context, projectID, deploymentID string) {
	_, normalizedID, membership, ok := h.projectAccess(c, projectID)
	if !ok {
		return
	}
	if membership.Role != "owner" && membership.Role != "developer" {
		c.JSON(http.StatusForbidden, apigen.Error{Message: "只有项目所有者和开发者可以激活 Worker 版本"})
		return
	}
	if h.workers == nil {
		c.JSON(http.StatusServiceUnavailable, apigen.Error{Message: "Worker 执行环境未配置"})
		return
	}
	id, err := uuid.Parse(deploymentID)
	if err != nil || id == uuid.Nil {
		c.JSON(http.StatusNotFound, apigen.Error{Message: "Worker 版本不存在"})
		return
	}
	op, err := h.workers.Activate(c.Request.Context(), uuid.MustParse(normalizedID), id)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, apigen.Error{Message: "Worker 版本不存在"})
	case errors.Is(err, database.ErrWorkerDeploymentNotReady), errors.Is(err, database.ErrRouteConflict):
		c.JSON(http.StatusConflict, apigen.Error{Message: err.Error()})
	case errors.Is(err, workerpublish.ErrRuntimeNotReady):
		c.JSON(http.StatusUnprocessableEntity, apigen.Error{Message: err.Error()})
	case err != nil:
		h.failure(c, err)
	default:
		c.JSON(http.StatusOK, operationResponse(op))
	}
}

func operationResponse(op operations.Operation) apigen.Operation {
	return apigen.Operation{Id: op.ID, Action: op.Action, Phase: string(op.Phase), Outcome: string(op.Outcome), CreatedAt: op.CreatedAt, UpdatedAt: op.UpdatedAt}
}

func deploymentLocation(projectID string, id uuid.UUID) string {
	return fmt.Sprintf("/api/v1/projects/%s/worker-deployments/%s", projectID, id)
}

func workerDeploymentResponse(result workerpublish.Result) apigen.WorkerDeployment {
	d, e, o := result.Deployment, result.Execution, result.Operation
	response := apigen.WorkerDeployment{
		Id: d.ID, ProjectId: d.ProjectID, WorkerId: d.WorkerID, ExecutionId: e.ID, OperationId: d.OperationID,
		ArtifactDigest: d.ArtifactDigest, ConfigDigest: d.ConfigDigest, Entry: d.Entry,
		CompatibilityDate: d.CompatibilityDate, RuntimeVersion: d.RuntimeVersion, RuntimeImage: d.RuntimeImage,
		CompatibilityFlags: d.CompatibilityFlags, Hostname: d.Hostname, CpuMillis: d.CPUMillis, MemoryBytes: d.MemoryBytes, State: d.State, Failure: d.Failure,
		ContainerId: e.ContainerID, ExecutionState: e.State, Generation: int64(e.Generation),
		OperationPhase: string(o.Phase), OperationOutcome: string(o.Outcome), CreatedAt: d.CreatedAt,
	}
	if e.LastExit.ObservedAt != nil {
		response.LastExit = &apigen.WorkerExecutionExit{
			Reason: e.LastExit.Reason, ExitCode: e.LastExit.Code, ObservedAt: *e.LastExit.ObservedAt, FinishedAt: e.LastExit.FinishedAt,
		}
		if e.LastExit.Detail != "" {
			response.LastExit.Detail = &e.LastExit.Detail
		}
	}
	return response
}
