package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// WorkerExit reports the latest observed interruption of a published execution.
type WorkerExit struct {
	Reason     string
	Code       *int
	FinishedAt *time.Time
	ObservedAt *time.Time
	Detail     string
}

// RecordWorkerExecutionObservation uses compare-and-swap to avoid applying an
// observation over a concurrent change in execution or ownership.
func (s *Store) RecordWorkerExecutionObservation(ctx context.Context, current WorkerExecution, state string, exit *WorkerExit) (WorkerExecution, error) {
	db := s.DB.WithContext(ctx)
	values := map[string]any{"state": state, "observation_version": gorm.Expr("observation_version + 1")}
	if exit != nil {
		values["last_exit_reason"] = exit.Reason
		values["last_exit_code"] = exit.Code
		values["last_exit_finished_at"] = exit.FinishedAt
		values["last_exit_observed_at"] = exit.ObservedAt
		values["last_exit_detail"] = exit.Detail
	}
	result := db.Model(&WorkerExecution{}).Where(
		"project_id = ? AND id = ? AND container_id = ? AND state = ? AND observation_version = ?",
		current.ProjectID, current.ID, current.ContainerID, current.State, current.ObservationVersion,
	).Updates(values)
	if result.Error != nil {
		return WorkerExecution{}, fmt.Errorf("record Worker execution %s observation: %w", current.ID, result.Error)
	}
	if result.RowsAffected == 0 {
		err := db.Where("project_id = ? AND id = ?", current.ProjectID, current.ID).Take(&current).Error
		if err != nil {
			return WorkerExecution{}, fmt.Errorf("reload Worker execution %s observation: %w", current.ID, err)
		}
		return current, nil
	}
	current.State = state
	current.ObservationVersion++
	if exit != nil {
		current.LastExit = *exit
	}
	return current, nil
}
