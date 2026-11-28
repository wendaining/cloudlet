// Package workerpublish publishes stateless Worker deployments through
// durable artifacts, capacity admission, real workerd and atomic Gateway routes.
package workerpublish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/artifacts"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/durableobjects"
	"github.com/wendaining/cloudlet/internal/executions"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/operations"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/workers"
)

const MaxModuleBytes = 1 << 20

var (
	namePattern              = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	ErrPublicationIncomplete = errors.New("Worker publication is incomplete")
	ErrRuntimeNotReady       = errors.New("Worker execution is not ready")
)

type Request struct {
	Module             string                 `json:"module"`
	CompatibilityDate  string                 `json:"compatibility_date"`
	Hostname           string                 `json:"hostname"`
	CompatibilityFlags []string               `json:"compatibility_flags,omitempty"`
	Environment        map[string]string      `json:"environment,omitempty"`
	Stage              bool                   `json:"stage,omitempty"`
	DurableObjects     []DurableObjectBinding `json:"durable_objects,omitempty"`
}

type DurableObjectBinding struct {
	ClassName string   `json:"class_name"`
	Bindings  []string `json:"bindings"`
}

func (r Request) namespaces(projectID uuid.UUID, worker string) ([]durableobjects.Namespace, error) {
	if len(r.DurableObjects) > 16 {
		return nil, fmt.Errorf("Worker supports up to 16 Durable Object classes: %w", fs.ErrInvalid)
	}
	var namespaces []durableobjects.Namespace
	for _, binding := range r.DurableObjects {
		if len(binding.ClassName) > 128 || len(binding.Bindings) > 16 {
			return nil, fmt.Errorf("Durable Object class or bindings exceed their limits: %w", fs.ErrInvalid)
		}
		for _, name := range binding.Bindings {
			if len(name) > 128 {
				return nil, fmt.Errorf("Durable Object binding exceeds 128 bytes: %w", fs.ErrInvalid)
			}
		}
		id := uuid.NewSHA1(projectID, []byte("cloudlet/durable-object/"+worker+"/"+binding.ClassName))
		namespaces = append(namespaces, durableobjects.Namespace{ID: id, ClassName: binding.ClassName, Bindings: binding.Bindings})
	}
	return namespaces, nil
}

func ValidateRequest(name, key string, request Request) (workers.RuntimeConfig, error) {
	if len(name) > 63 || !namePattern.MatchString(name) || len(key) > 256 || strings.TrimSpace(key) == "" || strings.ContainsRune(key, 0) || !utf8.ValidString(key) {
		return workers.RuntimeConfig{}, fmt.Errorf("Worker name and Idempotency-Key are invalid: %w", fs.ErrInvalid)
	}
	if len(request.Module) > MaxModuleBytes || len(request.Hostname) > 253 {
		return workers.RuntimeConfig{}, fmt.Errorf("Worker module or hostname exceeds its size limit: %w", fs.ErrInvalid)
	}
	if err := config.ValidateRoutes([]config.Route{{Host: request.Hostname, Target: "http://127.0.0.1:8080"}}); err != nil {
		return workers.RuntimeConfig{}, fmt.Errorf("validate Worker hostname: %w: %w", fs.ErrInvalid, err)
	}
	prepared, err := workers.PrepareModuleWithFlagsAndEnvironment([]byte(request.Module), request.CompatibilityDate, request.CompatibilityFlags, request.Environment)
	if err != nil {
		return prepared, fmt.Errorf("validate Worker module: %w: %w", fs.ErrInvalid, err)
	}
	return prepared, nil
}

type Result struct {
	Deployment database.WorkerDeployment
	Execution  database.WorkerExecution
	Operation  operations.Operation
}

type Service struct {
	store          *database.Store
	artifacts      *artifacts.Store
	docker         *client.Client
	containers     *containers.Adapter
	reservations   *capacity.Store
	executions     *executions.Service
	operations     *operations.Store
	publisher      *routes.Publisher
	configuration  config.WorkerRuntime
	instance       string
	image          string
	controller     *sql.Conn
	controlContext context.Context
	stopController context.CancelCauseFunc
	monitorDone    chan struct{}
	mu             sync.Mutex
}

// NewService verifies every configured dependency before enabling publication.
// A PostgreSQL session lock allows only one Worker controller for the host.
func NewService(ctx context.Context, store *database.Store, docker *client.Client, publisher *routes.Publisher, configuration config.WorkerRuntime, instance string) (service *Service, err error) {
	if store == nil || docker == nil || publisher == nil {
		return nil, fmt.Errorf("Worker publication requires database, Docker and Gateway: %w", fs.ErrInvalid)
	}
	if err := configuration.Validate(); err != nil {
		return nil, err
	}
	if err := capacity.ValidateHostID(configuration.HostID); err != nil {
		return nil, err
	}
	if err := (containers.Ownership{Environment: instance, Project: uuid.NewString(), Resource: uuid.NewString(), Generation: 1}).Validate(); err != nil {
		return nil, err
	}
	runtime, err := containers.New(ctx, docker)
	if err != nil {
		return nil, err
	}
	image, err := runtime.PrepareImage(ctx, workers.RuntimeImage)
	if err != nil {
		return nil, fmt.Errorf("prepare Worker runtime image: %w", err)
	}
	if err := os.MkdirAll(configuration.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create Worker artifact directory %q: %w", configuration.Directory, err)
	}
	files, err := artifacts.New(configuration.Directory, MaxModuleBytes)
	if err != nil {
		return nil, err
	}
	s := &Service{store: store, docker: docker, containers: runtime, artifacts: files, configuration: configuration, instance: instance, image: image.Reference, publisher: publisher}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
		}
	}()
	s.reservations, err = capacity.NewStore(store.DB)
	if err != nil {
		return nil, err
	}
	s.operations, err = operations.NewStore(store.DB)
	if err != nil {
		return nil, err
	}
	s.executions, err = executions.NewService(s.reservations, runtime, configuration.HostID)
	if err != nil {
		return nil, err
	}
	s.controller, err = store.SQL.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err = s.controller.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", "cloudlet-worker-controller:"+configuration.HostID).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("lock Worker controller: %w", err)
	}
	if !acquired {
		return nil, errors.New("another Worker controller already owns this host")
	}
	if _, err = s.reservations.SetHostCapacity(ctx, configuration.HostID, resources(configuration.HostBudget)); err != nil {
		return nil, err
	}
	if _, err = s.publisher.Sync(ctx); err != nil {
		return nil, err
	}
	s.controlContext, s.stopController = context.WithCancelCause(context.Background())
	s.monitorDone = make(chan struct{})
	go s.monitorController()
	return s, nil
}

func resources(b config.ResourceBudget) capacity.Resources {
	return capacity.Resources{CPUMillis: b.CPUMillis, MemoryBytes: b.MemoryBytes, Instances: b.Instances}
}

func (s *Service) Close() error {
	var err error
	if s.stopController != nil {
		s.stopController(context.Canceled)
		<-s.monitorDone
	}
	if s.controller != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, unlockErr := s.controller.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", "cloudlet-worker-controller:"+s.configuration.HostID)
		if unlockErr != nil {
			// Discard a connection whose advisory lock release is unconfirmed.
			unlockErr = errors.Join(unlockErr, s.controller.Raw(func(any) error { return driver.ErrBadConn }))
		}
		err = errors.Join(unlockErr, s.controller.Close())
	}
	return errors.Join(err, s.artifacts.Close())
}

func (s *Service) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cause := context.Cause(s.controlContext); cause != nil {
		return cause
	}
	if err := s.controller.PingContext(ctx); err != nil {
		s.stopController(fmt.Errorf("Worker controller connection failed: %w", err))
		return context.Cause(s.controlContext)
	}
	return nil
}

func (s *Service) monitorController() {
	defer close(s.monitorDone)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.controlContext.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.controlContext, time.Second)
			err := s.Ready(ctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Service) Get(ctx context.Context, projectID, deploymentID uuid.UUID) (Result, error) {
	deployment, execution, err := s.store.WorkerDeployment(ctx, projectID, deploymentID)
	if err != nil {
		return Result{}, err
	}
	if deployment.State == "active" && (execution.State == "running" || execution.LastExit.Reason != "") {
		execution, err = s.observeExecution(ctx, deployment, execution)
		if err != nil {
			return Result{}, err
		}
	}
	op, err := s.operations.Get(ctx, projectID, deployment.OperationID)
	return Result{Deployment: deployment, Execution: execution, Operation: op}, err
}

// Deploy publishes a first version or prepares its replacement for activation.
func (s *Service) Deploy(ctx context.Context, projectID uuid.UUID, name, key string, request Request) (result Result, created bool, err error) {
	prepared, err := ValidateRequest(name, key, request)
	if err != nil {
		return result, false, err
	}
	if projectID == uuid.Nil || s.publisher.ReservedHost(request.Hostname) {
		return result, false, fmt.Errorf("Worker project or hostname is unavailable: %w", fs.ErrInvalid)
	}
	namespaces, err := request.namespaces(projectID, name)
	if err != nil {
		return result, false, err
	}
	if len(namespaces) > 0 {
		prepared, err = workers.PrepareDurableModuleWithFlagsAndEnvironment(prepared.Module, prepared.CompatibilityDate, namespaces, prepared.CompatibilityFlags, request.Environment)
		if err != nil {
			return result, false, fmt.Errorf("validate Durable Object bindings: %w: %w", fs.ErrInvalid, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return result, false, fmt.Errorf("verify Worker controller ownership: %w", err)
	}
	request.Hostname = gateway.CanonicalHost(request.Hostname)
	request.CompatibilityFlags = prepared.CompatibilityFlags
	encoded, err := json.Marshal(request)
	if err != nil {
		return result, false, err
	}
	op, fresh, err := s.operations.Register(ctx, operations.Registration{ProjectID: projectID, Action: "worker.deploy:" + name, IdempotencyKey: key, RequestFingerprint: fmt.Sprintf("%x", sha256.Sum256(encoded))})
	if err != nil {
		return result, false, err
	}
	result.Operation = op
	if !fresh {
		result.Deployment, result.Execution, err = s.store.WorkerDeploymentByOperation(ctx, projectID, op.ID)
		if err != nil {
			return result, false, errors.Join(ErrPublicationIncomplete, err)
		}
		if op.Outcome != operations.InProgress {
			return result, false, nil
		}
		if result.Deployment.State == "active" || result.Deployment.State == "ready" {
			err = s.finish(ctx, &result)
			return result, false, err
		}
		return result, false, fmt.Errorf("%w: %s", ErrPublicationIncomplete, result.Deployment.Failure)
	}
	reserved, committed, recorded := false, false, false
	defer func() {
		if err != nil && context.Cause(s.controlContext) != nil {
			err = errors.Join(err, context.Cause(s.controlContext))
		}
		if err == nil || committed {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		cleaned := !reserved
		if reserved {
			cleanupErr := s.executions.Stop(cleanupCtx, executions.StopRequest{ExecutionID: result.Execution.ID, ProjectID: projectID, Ownership: s.ownership(result), ContainerID: result.Execution.ContainerID})
			cleaned = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		if recorded {
			err = errors.Join(err, s.store.FailWorkerDeployment(cleanupCtx, result.Deployment.ID, err, cleaned))
			result.Deployment.State, result.Deployment.Failure = "failed", err.Error()
			if cleaned {
				result.Execution.State = "stopped"
			}
		}
		if cleaned {
			advanceErr := s.advance(cleanupCtx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed})
			err = errors.Join(err, advanceErr)
		}
	}()
	var hostConflict bool
	if err = s.store.CheckWorkerPublication(ctx, projectID, name, request.Hostname, request.Stage); err != nil {
		return result, true, err
	}
	if !request.Stage {
		if err = s.store.DB.WithContext(ctx).Raw("SELECT EXISTS (SELECT 1 FROM project_routes WHERE host = ? AND (project_id <> ? OR path_prefix = '/'))", request.Hostname, projectID).Row().Scan(&hostConflict); err != nil {
			return result, true, err
		}
		if hostConflict {
			return result, true, database.ErrRouteConflict
		}
	}
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Saving, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	moduleDigest, err := s.artifacts.Put(bytes.NewReader(prepared.Module))
	if err != nil {
		return result, true, err
	}
	files := []containers.File{{Path: "worker/config.capnp", Data: prepared.Config}, {Path: "worker/worker.mjs", Data: prepared.Module}}
	for _, binding := range prepared.Environment {
		if _, err := s.artifacts.Put(strings.NewReader(binding.Value)); err != nil {
			return result, true, fmt.Errorf("save Worker environment value %q: %w", binding.Name, err)
		}
		files = append(files, containers.File{Path: "worker/" + binding.FilePath(), Data: []byte(binding.Value)})
	}
	configDigest, err := s.artifacts.Put(bytes.NewReader(prepared.Config))
	if err != nil {
		return result, true, err
	}
	deployment, workerExecution, err := s.store.CreateWorkerDeployment(ctx, name, database.WorkerDeployment{
		ID: uuid.New(), ProjectID: projectID, OperationID: op.ID, ArtifactDigest: moduleDigest, ConfigDigest: configDigest,
		Entry: prepared.Entry, CompatibilityDate: prepared.CompatibilityDate, RuntimeVersion: workers.RuntimeVersion, RuntimeImage: s.image,
		Hostname: request.Hostname, CompatibilityFlags: prepared.CompatibilityFlags, CPUMillis: 1000, MemoryBytes: 256 << 20, State: "pending",
	}, request.Stage)
	if err != nil {
		return result, true, err
	}
	result.Deployment, result.Execution = deployment, workerExecution
	recorded = true
	quota := s.configuration.ProjectQuota
	if err = s.store.DB.WithContext(ctx).Exec("INSERT INTO capacity_project_quotas (project_id, cpu_millis, memory_bytes, instances) VALUES (?, ?, ?, ?) ON CONFLICT (project_id) DO NOTHING", projectID, quota.CPUMillis, quota.MemoryBytes, quota.Instances).Error; err != nil {
		return result, true, err
	}
	ownership := s.ownership(result)
	_, _, err = s.reservations.Reserve(ctx, capacity.Request{ExecutionID: result.Execution.ID, HostID: s.configuration.HostID, ProjectID: projectID, Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 256 << 20, Instances: 1}, Binding: capacity.Binding{Environment: ownership.Environment, Project: ownership.Project, Resource: ownership.Resource, Generation: ownership.Generation, Image: s.image}})
	if err != nil {
		return result, true, err
	}
	reserved = true
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preflight, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	if _, err = workers.Preflight(ctx, s.docker, prepared); err != nil {
		return result, true, err
	}
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	var dataVolume *containers.DataVolume
	if len(namespaces) != 0 {
		dataVolume, err = s.claimDurableVolume(ctx, result.Execution, namespaces)
		if err != nil {
			return result, true, err
		}
	}
	execution, err := s.executions.Start(ctx, executions.StartRequest{ExecutionID: result.Execution.ID, ProjectID: projectID, Spec: containers.Spec{
		Ownership: ownership, Image: s.image, User: "1000:1000", Port: workers.HTTPPort, Access: containers.AccessPublished, Command: workers.ServeArgs(),
		Limits:     containers.Limits{CPUMillis: 1000, MemoryBytes: 256 << 20, PIDs: 128, OpenFiles: 1024},
		Files:      files,
		DataVolume: dataVolume,
	}})
	result.Execution.ContainerID = execution.Container.ID
	if result.Execution.ContainerID != "" {
		err = errors.Join(err, s.store.SetWorkerExecutionContainer(ctx, projectID, result.Execution.ID, result.Execution.ContainerID))
	}
	if err != nil {
		return result, true, err
	}
	target, err := s.waitReady(ctx, ownership, execution.Container.ID)
	if err != nil {
		return result, true, err
	}
	if request.Stage {
		if err = s.store.ReadyWorkerDeployment(ctx, result.Deployment, result.Execution); err != nil {
			checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer checkCancel()
			actual, _, checkErr := s.store.WorkerDeployment(checkCtx, projectID, result.Deployment.ID)
			committed = checkErr != nil || actual.State == "ready"
			return result, true, errors.Join(err, checkErr)
		}
		committed = true
		result.Deployment.State, result.Execution.State = "ready", "running"
		err = s.finish(ctx, &result)
		return result, true, err
	}
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Activating, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	if _, err = s.store.ActivateWorkerDeployment(ctx, result.Deployment, result.Execution, target); err != nil {
		// A commit response may be lost. Never delete an execution until the
		// committed active pointer is known to remain unchanged.
		checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer checkCancel()
		actual, _, checkErr := s.store.WorkerDeployment(checkCtx, projectID, result.Deployment.ID)
		if checkErr != nil || actual.State == "active" {
			committed = true
			err = errors.Join(err, checkErr)
		}
		return result, true, err
	}
	committed = true
	result.Deployment.State, result.Execution.State = "active", "running"
	err = s.finish(ctx, &result)
	return result, true, err
}

func (s *Service) ownership(result Result) containers.Ownership {
	return containers.Ownership{Environment: s.instance, Project: result.Deployment.ProjectID.String(), Resource: result.Deployment.WorkerID.String(), Generation: result.Execution.Generation}
}

func (s *Service) advance(ctx context.Context, result *Result, progress operations.Progress) error {
	op, err := s.operations.Advance(ctx, result.Operation.ProjectID, result.Operation.ID, result.Operation.Phase, progress)
	if err == nil {
		result.Operation = op
	}
	return err
}

func (s *Service) finish(ctx context.Context, result *Result) error {
	if result.Deployment.State == "active" {
		if _, err := s.publisher.Sync(ctx); err != nil {
			return fmt.Errorf("apply Worker Gateway route: %w", err)
		}
	}
	if result.Operation.Phase != operations.Finalizing {
		if err := s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.InProgress}); err != nil {
			return err
		}
	}
	return s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.Succeeded})
}

func (s *Service) waitReady(ctx context.Context, ownership containers.Ownership, containerID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	var last error
	for {
		observation, err := s.containers.Observe(ctx, ownership, containerID)
		if err != nil {
			if ctx.Err() != nil {
				return "", fmt.Errorf("%w: execution %s: %w", ErrRuntimeNotReady, containerID, errors.Join(err, last))
			}
			return "", err
		}
		if !observation.Running {
			return "", fmt.Errorf("%w: execution %s exited with status %d", ErrRuntimeNotReady, containerID, observation.ExitCode)
		}
		for _, port := range observation.Ports {
			if port.ContainerPort != workers.HTTPPort || port.Protocol != "tcp" {
				continue
			}
			target := "http://" + port.HostAddress()
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, target+"/", nil)
			if err != nil {
				return "", err
			}
			response, err := client.Do(request)
			if err == nil {
				closeErr := response.Body.Close()
				if closeErr != nil {
					return "", closeErr
				}
				if response.StatusCode >= 200 && response.StatusCode < 400 {
					return target, nil
				}
				last = fmt.Errorf("Worker readiness returned HTTP %d", response.StatusCode)
			} else {
				last = err
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("%w: execution %s: %w", ErrRuntimeNotReady, containerID, errors.Join(ctx.Err(), last))
		case <-ticker.C:
		}
	}
}
