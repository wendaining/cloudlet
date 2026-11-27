package workerpublish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
)

func (s *Service) observeExecution(ctx context.Context, deployment database.WorkerDeployment, execution database.WorkerExecution) (database.WorkerExecution, error) {
	observed, err := s.containers.Observe(ctx, s.ownership(Result{Deployment: deployment, Execution: execution}), execution.ContainerID)
	missing := errors.Is(err, containers.ErrNotFound)
	if err != nil && !missing {
		return database.WorkerExecution{}, fmt.Errorf("observe Worker execution %s: %w", execution.ID, err)
	}
	if !missing && observed.Running && !observed.Restarting {
		if execution.State == "running" {
			return execution, nil
		}
		return s.store.RecordWorkerExecutionObservation(ctx, execution, "running", nil)
	}
	exit := classifyExecutionExit(observed, missing)
	if execution.State == "stopped" && (missing || sameExecutionExit(exit, execution.LastExit)) {
		return execution, nil
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	exit.ObservedAt = &now
	return s.store.RecordWorkerExecutionObservation(ctx, execution, "stopped", &exit)
}

func classifyExecutionExit(observed containers.Observation, missing bool) database.WorkerExit {
	if missing {
		return database.WorkerExit{Reason: "container_missing"}
	}
	exit := database.WorkerExit{Detail: observed.StateError}
	if !observed.FinishedAt.IsZero() {
		// PostgreSQL timestamps retain microseconds. Compare with the same
		// precision when deciding whether this is a new observation.
		finishedAt := observed.FinishedAt.UTC().Truncate(time.Microsecond)
		exit.FinishedAt = &finishedAt
		exit.Code = &observed.ExitCode
	}
	switch {
	case observed.OOMKilled:
		exit.Reason = "oom_killed"
	case observed.StateError != "":
		exit.Reason = "docker_error"
	case exit.Code == nil:
		exit.Reason, exit.Detail = "not_running", observed.Status
	case *exit.Code != 0:
		exit.Reason = "nonzero_exit"
	default:
		exit.Reason = "normal_exit"
	}
	return exit
}

func sameExecutionExit(a, b database.WorkerExit) bool {
	if a.Reason != b.Reason || a.Detail != b.Detail || (a.Code == nil) != (b.Code == nil) ||
		(a.FinishedAt == nil) != (b.FinishedAt == nil) {
		return false
	}
	return (a.Code == nil || *a.Code == *b.Code) && (a.FinishedAt == nil || a.FinishedAt.Equal(*b.FinishedAt))
}
