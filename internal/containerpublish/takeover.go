package containerpublish

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
)

// Restore reconciles the active container executions of this controller with
// Docker before the service starts serving requests. A running container with
// matching ownership is adopted in place and keeps its execution identifier; a
// container stopped outside the platform is started once and probed for
// readiness before its route is restored. A recorded container that no longer
// exists is never recreated: its route is revoked, its capacity released and
// the container recorded as failed.
func (s *Service) Restore(ctx context.Context) error {
	takeovers, err := s.store.ActiveContainerExecutions(ctx, s.configuration.HostID, s.instance)
	if err != nil {
		return fmt.Errorf("read active container executions: %w", err)
	}
	for _, takeover := range takeovers {
		if err := s.restoreContainer(ctx, takeover); err != nil {
			return fmt.Errorf("take over container %s execution %s: %w", takeover.Container.ID, takeover.Execution.ID, err)
		}
	}
	if _, err := s.publisher.Sync(ctx); err != nil {
		return fmt.Errorf("apply container Gateway routes after takeover: %w", err)
	}
	return nil
}

func (s *Service) restoreContainer(ctx context.Context, takeover database.ContainerTakeover) error {
	execution := takeover.Execution
	binding, err := s.resolvePostgresBinding(ctx, execution.ProjectID, execution.PostgresBindingName, execution.PostgresSecretVersionID)
	if err != nil {
		return fmt.Errorf("resolve PostgreSQL binding: %w", err)
	}
	s3, err := s.resolveS3Binding(ctx, execution.ProjectID, execution.S3BindingName, execution.S3SecretVersionID)
	if err != nil {
		return fmt.Errorf("resolve S3 binding: %w", err)
	}
	spec := s.executionSpec(execution, binding, s3)
	adopted, found, err := s.executions.Adopt(ctx, executions.StartRequest{
		ExecutionID: execution.ID,
		ProjectID:   execution.ProjectID,
		Spec:        spec,
	})
	if err != nil {
		return fmt.Errorf("adopt managed container: %w", err)
	}
	if !found {
		return s.failTakeover(ctx, takeover, execution.DockerContainerID, errors.New("managed Docker container no longer exists"))
	}
	observation := adopted.Observation
	if adopted.Started {
		readiness, err := s.containers.WaitReady(ctx, spec.Ownership, adopted.Container.ID, containers.ReadinessProbe{
			Port: execution.Port, Path: execution.HealthPath,
			Timeout:  time.Duration(execution.HealthTimeoutMS) * time.Millisecond,
			Interval: healthInterval, Statuses: acceptedStatuses(),
		})
		if err != nil {
			return s.failTakeover(ctx, takeover, adopted.Container.ID, fmt.Errorf("wait for adopted container readiness: %w", err))
		}
		observation = readiness.Observation
	}
	if !observation.Running {
		return s.failTakeover(ctx, takeover, observation.ID, errors.New("managed Docker container is not running"))
	}
	target, err := publishedTarget(observation, execution.Port)
	if err != nil {
		return s.failTakeover(ctx, takeover, observation.ID, fmt.Errorf("read adopted container endpoint: %w", err))
	}
	if target == execution.Target && observation.ID == execution.DockerContainerID && takeover.RouteTarget == target {
		return nil
	}
	if _, err := s.store.UpdateContainerExecutionRuntime(ctx, takeover.Container, execution, observation.ID, target); err != nil {
		return fmt.Errorf("record adopted container endpoint: %w", err)
	}
	return nil
}

// failTakeover revokes the public route, stops the bound container, releases
// capacity and records the failed execution. A missing container makes the stop
// a no-op that still releases capacity. The saved data volume is preserved so a
// later restart can create a new execution.
func (s *Service) failTakeover(ctx context.Context, takeover database.ContainerTakeover, containerID string, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if reservation, err := s.reservations.GetReservation(cleanupCtx, takeover.Container.ProjectID, takeover.Execution.ID); err != nil {
		return errors.Join(cause, fmt.Errorf("read container execution reservation: %w", err))
	} else if reservation.ContainerID != "" {
		// Stop verifies the recorded identifier, which may differ from the
		// execution row when a previous update did not complete.
		containerID = reservation.ContainerID
	}
	if _, err := s.publisher.RemoveResource(cleanupCtx, takeover.Container.ProjectID.String(), takeover.Container.ID.String()); err != nil {
		return errors.Join(cause, fmt.Errorf("revoke container route: %w", err))
	}
	if err := s.executions.Stop(cleanupCtx, executions.StopRequest{
		ExecutionID: takeover.Execution.ID, ProjectID: takeover.Container.ProjectID,
		Ownership: s.takeoverOwnership(takeover), ContainerID: containerID,
	}); err != nil {
		return errors.Join(cause, fmt.Errorf("stop container execution: %w", err))
	}
	if err := s.store.FailContainerTakeover(cleanupCtx, takeover.Container.ProjectID, takeover.Container.ID, takeover.Execution.ID, cause); err != nil {
		return errors.Join(cause, err)
	}
	return nil
}

func (s *Service) takeoverOwnership(takeover database.ContainerTakeover) containers.Ownership {
	return containers.Ownership{
		Environment: s.instance,
		Project:     takeover.Container.ProjectID.String(),
		Resource:    takeover.Container.ID.String(),
		Generation:  takeover.Execution.Generation,
	}
}

// executionSpec rebuilds the immutable Docker specification of one execution so
// adoption verifies the same image, limits, files and data volume that created
// the container.
func (s *Service) executionSpec(execution database.ContainerExecution, binding, s3 runtimeBinding) containers.Spec {
	var dataVolume *containers.DataVolume
	if execution.VolumeName != "" {
		dataVolume = &containers.DataVolume{
			Name: execution.VolumeName, MountPath: execution.VolumeMountPath, Directory: execution.VolumeDirectory,
		}
	}
	return containers.Spec{
		Ownership: containers.Ownership{
			Environment: s.instance, Project: execution.ProjectID.String(),
			Resource: execution.ContainerID.String(), Generation: execution.Generation,
		},
		Image: execution.Image, User: execution.UserName,
		Command: append([]string(nil), execution.Command...),
		Env:     s3.environment(binding.environment(execution.Env)),
		Files:   append(binding.files, s3.files...),
		Port:    execution.Port, Access: containers.AccessPublished,
		DataVolume: dataVolume,
		Limits: containers.Limits{
			CPUMillis: execution.CPUMillis, MemoryBytes: execution.MemoryBytes,
			PIDs: execution.PIDs, OpenFiles: execution.OpenFiles,
		},
	}
}
