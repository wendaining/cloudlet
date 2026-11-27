package workerpublish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
	"github.com/wendaining/cloudlet/internal/operations"
	"gorm.io/gorm"
)

// Activate switches a ready Worker deployment once. Retries reuse its activation
// operation and can finish cleanup after the route transaction has committed.
func (s *Service) Activate(ctx context.Context, projectID, deploymentID uuid.UUID) (operations.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return operations.Operation{}, fmt.Errorf("verify Worker controller ownership: %w", err)
	}
	candidate, err := s.Get(ctx, projectID, deploymentID)
	if err != nil {
		return operations.Operation{}, err
	}
	if candidate.Deployment.State != "ready" && candidate.Deployment.State != "active" && candidate.Deployment.State != "retired" {
		return operations.Operation{}, database.ErrWorkerDeploymentNotReady
	}
	if candidate.Operation.Outcome != operations.Succeeded {
		return operations.Operation{}, database.ErrWorkerDeploymentNotReady
	}
	if candidate.Deployment.State != "ready" {
		activation, err := s.store.WorkerActivationByDeployment(ctx, projectID, deploymentID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return operations.Operation{}, database.ErrWorkerDeploymentNotReady
		}
		if err != nil {
			return operations.Operation{}, err
		}
		op, err := s.operations.Get(ctx, projectID, activation.OperationID)
		if err != nil || op.Outcome == operations.Succeeded {
			return op, err
		}
		return s.finishActivation(ctx, candidate.Deployment.Hostname, activation, op)
	}

	target, err := s.waitReady(ctx, s.ownership(candidate), candidate.Execution.ContainerID)
	if err != nil {
		return operations.Operation{}, err
	}
	op, _, err := s.operations.Register(ctx, operations.Registration{
		ProjectID: projectID, Action: "worker.activate", IdempotencyKey: deploymentID.String(), RequestFingerprint: deploymentID.String(),
	})
	if err != nil {
		return op, err
	}
	if op.Phase == operations.Received {
		op, err = s.operations.Advance(ctx, projectID, op.ID, op.Phase, operations.Progress{Phase: operations.Activating, Outcome: operations.InProgress})
		if err != nil {
			return op, err
		}
	}
	activation, err := s.store.SwitchWorkerDeployment(ctx, candidate.Deployment, op.ID, target)
	if err != nil {
		return op, fmt.Errorf("switch Worker deployment %s: %w", deploymentID, err)
	}
	return s.finishActivation(ctx, candidate.Deployment.Hostname, activation, op)
}

func (s *Service) finishActivation(ctx context.Context, hostname string, activation database.WorkerActivation, op operations.Operation) (operations.Operation, error) {
	if _, err := s.publisher.Sync(ctx); err != nil {
		return op, fmt.Errorf("apply activated Worker route for %s: %w", hostname, err)
	}
	if op.Phase != operations.Finalizing {
		updated, err := s.operations.Advance(ctx, op.ProjectID, op.ID, op.Phase, operations.Progress{Phase: operations.Finalizing, Outcome: operations.InProgress})
		if err != nil {
			return op, err
		}
		op = updated
	}
	previous, err := s.Get(ctx, op.ProjectID, activation.PreviousDeploymentID)
	if err != nil {
		return op, err
	}
	if previous.Execution.State != "stopped" {
		drainCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.publisher.DrainProxy(drainCtx, hostname, activation.PreviousTarget)
		cancel()
		if err != nil && (!errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil) {
			return op, fmt.Errorf("drain retired Worker %s: %w", previous.Deployment.ID, err)
		}
		if err := s.executions.Stop(ctx, executions.StopRequest{ExecutionID: previous.Execution.ID, ProjectID: op.ProjectID, Ownership: s.ownership(previous), ContainerID: previous.Execution.ContainerID}); err != nil {
			return op, err
		}
		if err := s.store.StopRetiredWorkerExecution(ctx, op.ProjectID, previous.Execution.ID); err != nil {
			return op, err
		}
	}
	return s.operations.Advance(ctx, op.ProjectID, op.ID, op.Phase, operations.Progress{Phase: operations.Finalizing, Outcome: operations.Succeeded})
}
