// Package containerpublish creates a managed HTTP container through capacity
// admission, real Docker readiness and an atomic Gateway route.
package containerpublish

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
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/executions"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/operations"
	"github.com/wendaining/cloudlet/internal/routes"
	"gorm.io/gorm"
)

const (
	// MaxRequestBytes bounds one create request including command and environment.
	MaxRequestBytes = 64 << 10
	// DefaultUser is the non-root identity used when the request omits a user.
	DefaultUser = "1000:1000"
	// DefaultHealthPath is probed when the request omits a health check path.
	DefaultHealthPath = "/"
	// DefaultHealthTimeout is the readiness deadline when the request omits one.
	DefaultHealthTimeout = 15 * time.Second
	// MinHealthTimeout is the shortest accepted readiness deadline.
	MinHealthTimeout = time.Second
	// MaxHealthTimeout is the longest accepted readiness deadline.
	MaxHealthTimeout = time.Minute

	containerPIDs      = 256
	containerOpenFiles = 1024
	healthInterval     = 100 * time.Millisecond
)

var (
	namePattern              = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	ErrPublicationIncomplete = errors.New("容器创建尚未完成")
	ErrRuntimeNotReady       = errors.New("容器执行尚未就绪")
	ErrRecordedFailure       = errors.New("容器创建已失败")
	ErrLifecycleState        = errors.New("容器状态不允许该操作")
	ErrLifecycleFailed       = errors.New("容器生命周期操作失败")
	ErrRuntimeStop           = errors.New("容器执行停止失败")
	ErrDataVolumeMissing     = errors.New("容器数据卷不存在")
)

// RecordedFailure returns a failure that happened before a container execution
// row was written. Replaying the same idempotency key returns this cause.
type RecordedFailure struct {
	Failure string
}

func (e *RecordedFailure) Error() string { return e.Failure }

func (e *RecordedFailure) Unwrap() error { return ErrRecordedFailure }

// DataVolumeRequest declares one writable data volume for the container.
type DataVolumeRequest struct {
	MountPath string `json:"mount_path"`
	Directory string `json:"directory"`
}

// Request is one container creation request. Image must be an immutable
// repository@digest reference. The service applies defaults for User,
// HealthCheckPath and HealthCheckTimeoutMS.
type Request struct {
	Name                 string             `json:"name"`
	Image                string             `json:"image"`
	Port                 uint16             `json:"port"`
	Hostname             string             `json:"hostname"`
	CPUMillis            int64              `json:"cpu_millis"`
	MemoryBytes          int64              `json:"memory_bytes"`
	User                 string             `json:"user,omitempty"`
	Command              []string           `json:"command,omitempty"`
	Env                  []string           `json:"env,omitempty"`
	HealthCheckPath      string             `json:"health_check_path,omitempty"`
	HealthCheckTimeoutMS int64              `json:"health_check_timeout_ms,omitempty"`
	DataVolume           *DataVolumeRequest `json:"data_volume,omitempty"`

	PostgresBinding *string `json:"postgres_binding,omitempty"`
	S3Binding       *string `json:"s3_binding,omitempty"`

	imageDigest string
	volumeName  string
}

// ValidateRequest validates and normalizes one request without contacting
// Docker Engine. The returned image is the canonical repository@digest form.
func ValidateRequest(request Request) (Request, error) {
	if len(request.Name) > 63 || !namePattern.MatchString(request.Name) {
		return Request{}, fmt.Errorf("container name must contain 1 to 63 lowercase letters, digits or single hyphens: %w", fs.ErrInvalid)
	}
	if len(request.Command) > 64 || len(request.Env) > 128 {
		return Request{}, fmt.Errorf("container command or environment exceeds its item limit: %w", fs.ErrInvalid)
	}
	for _, binding := range []struct {
		name                    *string
		kind, file, environment string
	}{
		{request.PostgresBinding, "PostgreSQL", postgresURLPath, postgresURLFileEnv},
		{request.S3Binding, "S3", s3ConfigPath, s3ConfigFileEnv},
	} {
		if binding.name == nil {
			continue
		}
		credentialPath := "/" + binding.file
		if request.DataVolume != nil && (request.DataVolume.MountPath == credentialPath ||
			strings.HasPrefix(request.DataVolume.MountPath, credentialPath+"/")) {
			return Request{}, fmt.Errorf("container data volume cannot use the %s credential file path: %w", binding.kind, fs.ErrInvalid)
		}
		if len(*binding.name) > 63 || !namePattern.MatchString(*binding.name) {
			return Request{}, fmt.Errorf("invalid %s binding name: %w", binding.kind, fs.ErrInvalid)
		}
		for _, entry := range request.Env {
			key, _, _ := strings.Cut(entry, "=")
			if key == binding.environment {
				return Request{}, fmt.Errorf("environment key %s is reserved for the %s binding: %w", key, binding.kind, fs.ErrInvalid)
			}
		}
	}
	if len(request.User) > 128 {
		return Request{}, fmt.Errorf("container user exceeds 128 bytes: %w", fs.ErrInvalid)
	}
	canonical, digest, err := containers.ParseImageReference(request.Image)
	if err != nil {
		return Request{}, fmt.Errorf("validate container image: %w: %w", fs.ErrInvalid, err)
	}
	if !strings.HasPrefix(digest, "sha256:") {
		return Request{}, fmt.Errorf("container image requires a sha256 digest: %w", fs.ErrInvalid)
	}
	request.Image = canonical
	request.imageDigest = digest
	if request.Port == 0 {
		return Request{}, fmt.Errorf("container port must be between 1 and 65535: %w", fs.ErrInvalid)
	}
	if len(request.Hostname) > 253 {
		return Request{}, fmt.Errorf("container hostname exceeds 253 bytes: %w", fs.ErrInvalid)
	}
	if err := config.ValidateRoutes([]config.Route{{Host: request.Hostname, Target: "http://127.0.0.1:1"}}); err != nil {
		return Request{}, fmt.Errorf("validate container hostname: %w: %w", fs.ErrInvalid, err)
	}
	request.Hostname = gateway.CanonicalHost(request.Hostname)
	if request.Command == nil {
		request.Command = []string{}
	}
	if request.Env == nil {
		request.Env = []string{}
	}
	if request.User == "" {
		request.User = DefaultUser
	}
	if request.DataVolume != nil {
		volume := containers.DataVolume{Name: "validation", MountPath: request.DataVolume.MountPath, Directory: request.DataVolume.Directory}
		if err := containers.ValidateDataVolume(volume, request.User); err != nil {
			return Request{}, fmt.Errorf("validate container data volume: %w: %w", fs.ErrInvalid, err)
		}
	}
	if request.HealthCheckPath == "" {
		request.HealthCheckPath = DefaultHealthPath
	}
	if request.HealthCheckTimeoutMS == 0 {
		request.HealthCheckTimeoutMS = int64(DefaultHealthTimeout / time.Millisecond)
	}
	timeout := time.Duration(request.HealthCheckTimeoutMS) * time.Millisecond
	if timeout < MinHealthTimeout || timeout > MaxHealthTimeout {
		return Request{}, fmt.Errorf("container health check timeout must be between %s and %s: %w", MinHealthTimeout, MaxHealthTimeout, fs.ErrInvalid)
	}
	probe := containers.ReadinessProbe{
		Port: request.Port, Path: request.HealthCheckPath, Timeout: timeout,
		Interval: healthInterval, Statuses: acceptedStatuses(),
	}
	if err := containers.ValidateReadinessProbe(probe); err != nil {
		return Request{}, fmt.Errorf("validate container health check: %w: %w", fs.ErrInvalid, err)
	}
	spec := containers.Spec{
		Ownership: containers.Ownership{Environment: "validate", Project: "validate", Resource: "validate", Generation: 1},
		Image:     request.Image, User: request.User, Command: request.Command, Env: request.Env,
		Port: request.Port, Access: containers.AccessPublished,
		Limits: containers.Limits{CPUMillis: request.CPUMillis, MemoryBytes: request.MemoryBytes, PIDs: containerPIDs, OpenFiles: containerOpenFiles},
	}
	if err := spec.Validate(); err != nil {
		return Request{}, fmt.Errorf("validate container request: %w: %w", fs.ErrInvalid, err)
	}
	return request, nil
}

func acceptedStatuses() []int {
	statuses := make([]int, 0, 200)
	for status := http.StatusOK; status < http.StatusBadRequest; status++ {
		statuses = append(statuses, status)
	}
	return statuses
}

// Result is the durable container state returned by Create and Get.
type Result struct {
	Container database.Container
	Execution database.ContainerExecution
	Operation operations.Operation
}

type Service struct {
	store          *database.Store
	containers     *containers.Adapter
	reservations   *capacity.Store
	executions     *executions.Service
	operations     *operations.Store
	publisher      *routes.Publisher
	configuration  config.ContainerRuntime
	instance       string
	secretKey      []byte
	controller     *sql.Conn
	controlContext context.Context
	stopController context.CancelCauseFunc
	monitorDone    chan struct{}
	mu             sync.Mutex
}

// NewService verifies every configured dependency before enabling container
// creation. A PostgreSQL session lock allows only one container controller for
// the host.
func NewService(ctx context.Context, store *database.Store, docker *client.Client, publisher *routes.Publisher, configuration config.ContainerRuntime, instance string, secretKey []byte) (service *Service, err error) {
	if store == nil || docker == nil || publisher == nil {
		return nil, fmt.Errorf("container creation requires database, Docker and route publisher: %w", fs.ErrInvalid)
	}
	if len(secretKey) != 0 && len(secretKey) != 32 {
		return nil, fmt.Errorf("container project secret key must be 32 bytes: %w", fs.ErrInvalid)
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
	s := &Service{store: store, containers: runtime, configuration: configuration, instance: instance, publisher: publisher, secretKey: bytes.Clone(secretKey)}
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
	if err = s.controller.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", "cloudlet-container-controller:"+configuration.HostID).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("lock container controller: %w", err)
	}
	if !acquired {
		return nil, errors.New("another container controller already owns this host")
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
		_, unlockErr := s.controller.ExecContext(ctx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", "cloudlet-container-controller:"+s.configuration.HostID)
		if unlockErr != nil {
			// Discard a connection whose advisory lock release is unconfirmed.
			unlockErr = errors.Join(unlockErr, s.controller.Raw(func(any) error { return driver.ErrBadConn }))
		}
		err = errors.Join(unlockErr, s.controller.Close())
	}
	return err
}

func (s *Service) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cause := context.Cause(s.controlContext); cause != nil {
		return cause
	}
	if err := s.controller.PingContext(ctx); err != nil {
		s.stopController(fmt.Errorf("container controller connection failed: %w", err))
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

func (s *Service) Get(ctx context.Context, projectID, containerID uuid.UUID) (Result, error) {
	container, execution, err := s.store.ContainerResult(ctx, projectID, containerID)
	if err != nil {
		return Result{}, err
	}
	op, err := s.operations.Get(ctx, projectID, execution.OperationID)
	return Result{Container: container, Execution: execution, Operation: op}, err
}

// Create admits and starts one real container, waits for its HTTP readiness and
// activates its Gateway route. Repeated requests reuse the durable Operation.
func (s *Service) Create(ctx context.Context, projectID uuid.UUID, key string, request Request) (result Result, created bool, err error) {
	prepared, err := ValidateRequest(request)
	if err != nil {
		return result, false, err
	}
	if projectID == uuid.Nil || len(key) > 256 || strings.TrimSpace(key) == "" || strings.ContainsRune(key, 0) || !utf8.ValidString(key) {
		return result, false, fmt.Errorf("container project and Idempotency-Key are invalid: %w", fs.ErrInvalid)
	}
	if s.publisher.ReservedHost(prepared.Hostname) {
		return result, false, fmt.Errorf("container hostname %q is reserved by startup configuration: %w", prepared.Hostname, fs.ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return result, false, fmt.Errorf("verify container controller ownership: %w", err)
	}
	binding, err := s.resolvePostgresBinding(ctx, projectID, prepared.PostgresBinding, nil)
	if err != nil {
		return result, false, err
	}
	s3, err := s.resolveS3Binding(ctx, projectID, prepared.S3Binding, nil)
	if err != nil {
		return result, false, err
	}
	encoded, err := json.Marshal(prepared)
	if err != nil {
		return result, false, err
	}
	op, fresh, err := s.operations.Register(ctx, operations.Registration{
		ProjectID: projectID, Action: "container.create:" + prepared.Name,
		IdempotencyKey: key, RequestFingerprint: fmt.Sprintf("%x", sha256.Sum256(encoded)),
	})
	if err != nil {
		return result, false, err
	}
	result.Operation = op
	if !fresh {
		return s.resume(ctx, projectID, &result)
	}
	reserved, activated, recorded := false, false, false
	var dataVolume *containers.DataVolume
	volumeCreated := false
	defer func() {
		if err != nil && context.Cause(s.controlContext) != nil {
			err = errors.Join(err, context.Cause(s.controlContext))
		}
		if err == nil || activated {
			return
		}
		cause := err
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		cleaned := !reserved
		if reserved {
			cleanupErr := s.executions.Stop(cleanupCtx, executions.StopRequest{
				ExecutionID: result.Execution.ID, ProjectID: projectID,
				Ownership: s.ownership(result), ContainerID: result.Execution.DockerContainerID,
			})
			cleaned = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		if recorded && cleaned {
			err = errors.Join(err, s.store.FailContainerExecution(cleanupCtx, projectID, result.Container.ID, result.Execution.ID, cause))
			result.Container.State, result.Container.Failure = "failed", cause.Error()
			result.Execution.State, result.Execution.Failure = "failed", cause.Error()
		}
		if cleaned && dataVolume != nil && volumeCreated {
			err = errors.Join(err, s.containers.RemoveDataVolume(cleanupCtx, s.ownership(result), *dataVolume))
		}
		if cleaned {
			if !recorded {
				err = errors.Join(err, s.store.RecordContainerOperationFailure(cleanupCtx, projectID, result.Operation.ID, cause))
			}
			err = errors.Join(err, s.advance(cleanupCtx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed}))
		}
	}()
	if prepared.DataVolume != nil {
		prepared.volumeName = containers.DataVolumeName(s.instance, projectID.String(), prepared.Name)
	}
	if err = s.store.CheckContainerPublication(ctx, projectID, prepared.Name); err != nil {
		return result, true, err
	}
	if _, latest, lookupErr := s.store.ContainerByName(ctx, projectID, prepared.Name); lookupErr == nil {
		if latest.VolumeName != prepared.volumeName || latest.VolumeMountPath != dataVolumeMountPath(prepared) || latest.VolumeDirectory != dataVolumeDirectory(prepared) {
			return result, true, database.ErrContainerVolumeConflict
		}
	} else if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return result, true, lookupErr
	}
	if err = s.checkHostConflict(ctx, projectID, prepared.Hostname); err != nil {
		return result, true, err
	}
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Saving, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	container, execution, err := s.store.CreateContainerExecution(ctx, prepared.Name, database.ContainerExecution{
		ID: uuid.New(), ProjectID: projectID, OperationID: op.ID,
		Image: prepared.Image, ImageDigest: prepared.imageDigest, Port: prepared.Port,
		Hostname: prepared.Hostname, PathPrefix: "/", CPUMillis: prepared.CPUMillis,
		MemoryBytes: prepared.MemoryBytes, PIDs: containerPIDs, OpenFiles: containerOpenFiles,
		UserName: prepared.User, Command: prepared.Command, Env: prepared.Env,
		PostgresBindingName: binding.name, PostgresSecretVersionID: binding.secretVersionID,
		S3BindingName: s3.name, S3SecretVersionID: s3.secretVersionID,
		HealthPath: prepared.HealthCheckPath, HealthTimeoutMS: prepared.HealthCheckTimeoutMS,
		VolumeName:      prepared.volumeName,
		VolumeMountPath: dataVolumeMountPath(prepared),
		VolumeDirectory: dataVolumeDirectory(prepared),
	})
	if err != nil {
		return result, true, err
	}
	result.Container, result.Execution = container, execution
	recorded = true
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	image, err := s.containers.PrepareImage(ctx, prepared.Image)
	if err != nil {
		return result, true, err
	}
	if image.Digest != prepared.imageDigest {
		return result, true, fmt.Errorf("prepared image digest %s does not match requested digest %s: %w", image.Digest, prepared.imageDigest, containers.ErrImageDigestMismatch)
	}
	ownership := s.ownership(result)
	if prepared.DataVolume != nil {
		dataVolume = &containers.DataVolume{
			Name: prepared.volumeName, MountPath: prepared.DataVolume.MountPath, Directory: prepared.DataVolume.Directory,
		}
		volumeCreated, err = s.containers.EnsureDataVolume(ctx, ownership, *dataVolume)
		if err != nil {
			return result, true, err
		}
	}
	if err = s.reservations.EnsureProjectQuota(ctx, projectID, resources(s.configuration.ProjectQuota)); err != nil {
		return result, true, err
	}
	_, _, err = s.reservations.Reserve(ctx, capacity.Request{
		ExecutionID: result.Execution.ID, HostID: s.configuration.HostID, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: prepared.CPUMillis, MemoryBytes: prepared.MemoryBytes, Instances: 1},
		Binding: capacity.Binding{
			Environment: ownership.Environment, Project: ownership.Project, Resource: ownership.Resource,
			Generation: ownership.Generation, Image: image.Reference,
		},
	})
	if err != nil {
		return result, true, err
	}
	reserved = true
	executionResult, err := s.executions.Start(ctx, executions.StartRequest{ExecutionID: result.Execution.ID, ProjectID: projectID, Spec: containers.Spec{
		Ownership: ownership, Image: image.Reference, User: prepared.User,
		Command: prepared.Command, Env: s3.environment(binding.environment(prepared.Env)), Files: append(binding.files, s3.files...), Port: prepared.Port,
		Access: containers.AccessPublished, DataVolume: dataVolume,
		Limits: containers.Limits{CPUMillis: prepared.CPUMillis, MemoryBytes: prepared.MemoryBytes, PIDs: containerPIDs, OpenFiles: containerOpenFiles},
	}})
	result.Execution.DockerContainerID = executionResult.Container.ID
	if result.Execution.DockerContainerID != "" {
		err = errors.Join(err, s.store.SetContainerExecutionDockerID(ctx, projectID, result.Execution.ID, result.Execution.DockerContainerID))
	}
	if err != nil {
		return result, true, fmt.Errorf("%w: %w", ErrRuntimeNotReady, err)
	}
	if dataVolume != nil && executionResult.Observation.WritableVolumes[dataVolume.MountPath] != dataVolume.Name {
		return result, true, fmt.Errorf("container data volume %s is not mounted at %s: %w", dataVolume.Name, dataVolume.MountPath, fs.ErrInvalid)
	}
	readiness, err := s.containers.WaitReady(ctx, ownership, executionResult.Container.ID, containers.ReadinessProbe{
		Port: prepared.Port, Path: prepared.HealthCheckPath, Timeout: time.Duration(prepared.HealthCheckTimeoutMS) * time.Millisecond,
		Interval: healthInterval, Statuses: acceptedStatuses(),
	})
	if err != nil {
		return result, true, fmt.Errorf("%w: %w", ErrRuntimeNotReady, err)
	}
	target, err := publishedTarget(readiness.Observation, prepared.Port)
	if err != nil {
		return result, true, err
	}
	if err := config.ValidateRoutes([]config.Route{{Host: prepared.Hostname, Target: target}}); err != nil {
		return result, true, fmt.Errorf("validate container route target: %w: %w", fs.ErrInvalid, err)
	}
	if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Activating, Outcome: operations.InProgress}); err != nil {
		return result, true, err
	}
	if _, err = s.store.ActivateContainer(ctx, result.Container, result.Execution, target); err != nil {
		// A commit response may be lost. Never delete an execution until the
		// committed active pointer is known to remain unchanged.
		checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer checkCancel()
		actual, _, checkErr := s.store.ContainerResult(checkCtx, projectID, result.Container.ID)
		if checkErr != nil || actual.State == "active" {
			activated = true
			err = errors.Join(err, checkErr)
		}
		return result, true, err
	}
	activated = true
	result.Container.State, result.Execution.State = "active", "running"
	result.Execution.Target = target
	err = s.finish(ctx, &result)
	return result, true, err
}

func (s *Service) resume(ctx context.Context, projectID uuid.UUID, result *Result) (Result, bool, error) {
	container, execution, err := s.store.ContainerByOperation(ctx, projectID, result.Operation.ID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			failure, found, lookupErr := s.store.ContainerOperationFailure(ctx, projectID, result.Operation.ID)
			if lookupErr != nil {
				return *result, false, errors.Join(ErrPublicationIncomplete, lookupErr)
			}
			if found {
				return *result, false, &RecordedFailure{Failure: failure}
			}
		}
		return *result, false, errors.Join(ErrPublicationIncomplete, err)
	}
	result.Container, result.Execution = container, execution
	if container.State == "active" {
		if result.Operation.Outcome == operations.InProgress {
			err = s.finish(ctx, result)
		}
		return *result, false, err
	}
	if container.State == "failed" {
		if result.Operation.Outcome == operations.InProgress {
			err = s.advance(ctx, result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed})
		}
		return *result, false, err
	}
	if result.Operation.Outcome == operations.Failed {
		return *result, false, nil
	}
	return *result, false, fmt.Errorf("%w: %s", ErrPublicationIncomplete, container.Failure)
}

func (s *Service) ownership(result Result) containers.Ownership {
	return containers.Ownership{
		Environment: s.instance, Project: result.Container.ProjectID.String(),
		Resource: result.Container.ID.String(), Generation: result.Execution.Generation,
	}
}

func (s *Service) checkHostConflict(ctx context.Context, projectID uuid.UUID, host string) error {
	var conflict bool
	if err := s.store.DB.WithContext(ctx).Raw(
		"SELECT EXISTS (SELECT 1 FROM project_routes WHERE host = ? AND (project_id <> ? OR path_prefix = '/'))",
		host, projectID,
	).Row().Scan(&conflict); err != nil {
		return fmt.Errorf("check container host %q: %w", host, err)
	}
	if conflict {
		return database.ErrRouteConflict
	}
	return nil
}

func (s *Service) advance(ctx context.Context, result *Result, progress operations.Progress) error {
	op, err := s.operations.Advance(ctx, result.Operation.ProjectID, result.Operation.ID, result.Operation.Phase, progress)
	if err == nil {
		result.Operation = op
	}
	return err
}

func (s *Service) finish(ctx context.Context, result *Result) error {
	if _, err := s.publisher.Sync(ctx); err != nil {
		return fmt.Errorf("apply container Gateway route: %w", err)
	}
	if result.Operation.Phase != operations.Finalizing {
		if err := s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.InProgress}); err != nil {
			return err
		}
	}
	return s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.Succeeded})
}

// Stop removes the public route, stops and removes the Docker container, then
// releases capacity. Repeated calls with the same key return the stopped state.
func (s *Service) Stop(ctx context.Context, projectID, containerID uuid.UUID, key string) (result Result, err error) {
	if err := validateLifecycleRequest(projectID, containerID, key); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return result, fmt.Errorf("verify container controller ownership: %w", err)
	}
	op, fresh, err := s.registerLifecycle(ctx, projectID, containerID, "container.stop", key)
	if err != nil {
		return result, err
	}
	result.Operation = op
	container, execution, err := s.store.ContainerResult(ctx, projectID, containerID)
	if err != nil {
		if fresh && errors.Is(err, gorm.ErrRecordNotFound) {
			err = errors.Join(err, s.advance(ctx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed}))
		}
		return result, err
	}
	result.Container, result.Execution = container, execution
	if op.Outcome == operations.Succeeded {
		return result, nil
	}
	if op.Outcome == operations.Failed {
		return result, fmt.Errorf("%w: %s", ErrLifecycleFailed, container.Failure)
	}
	if container.State == "pending" {
		return result, database.ErrContainerPublicationPending
	}
	if container.State != "active" && container.State != "stopped" && container.State != "failed" {
		return result, fmt.Errorf("%w: %s", ErrLifecycleState, container.State)
	}
	if result.Operation.Phase == operations.Received {
		if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}); err != nil {
			return result, err
		}
	}
	if err = s.stopContainer(ctx, &result); err != nil {
		return result, err
	}
	return result, s.finishLifecycle(ctx, &result)
}

// Restart starts a new execution from the latest stored configuration, waits
// for HTTP readiness and reactivates the public route. It is allowed from a
// stopped or failed container; a new request against an active container is a
// conflict, while a retried request completes its recorded operation.
func (s *Service) Restart(ctx context.Context, projectID, containerID uuid.UUID, key string) (result Result, err error) {
	if err := validateLifecycleRequest(projectID, containerID, key); err != nil {
		return result, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return result, fmt.Errorf("verify container controller ownership: %w", err)
	}
	op, fresh, err := s.registerLifecycle(ctx, projectID, containerID, "container.restart", key)
	if err != nil {
		return result, err
	}
	result.Operation = op
	container, execution, err := s.store.ContainerResult(ctx, projectID, containerID)
	if err != nil {
		if fresh && errors.Is(err, gorm.ErrRecordNotFound) {
			err = errors.Join(err, s.advance(ctx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed}))
		}
		return result, err
	}
	result.Container, result.Execution = container, execution
	if op.Outcome == operations.Succeeded {
		return result, nil
	}
	if op.Outcome == operations.Failed {
		return result, fmt.Errorf("%w: %s", ErrLifecycleFailed, container.Failure)
	}
	if container.State == "pending" {
		return result, database.ErrContainerPublicationPending
	}
	if container.State == "active" {
		if fresh {
			return result, database.ErrContainerAlreadyActive
		}
		if result.Operation.Outcome == operations.InProgress {
			err = s.finish(ctx, &result)
		}
		return result, err
	}
	if container.State != "stopped" && container.State != "failed" {
		return result, fmt.Errorf("%w: %s", ErrLifecycleState, container.State)
	}
	if !fresh {
		_, prior, priorErr := s.store.ContainerByOperation(ctx, projectID, op.ID)
		if priorErr == nil {
			if prior.State == "failed" {
				advanceErr := s.advance(ctx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed})
				return result, errors.Join(fmt.Errorf("%w: %s", ErrLifecycleFailed, prior.Failure), advanceErr)
			}
			return result, fmt.Errorf("%w: restart execution is %s", ErrLifecycleFailed, prior.State)
		}
		if !errors.Is(priorErr, gorm.ErrRecordNotFound) {
			return result, priorErr
		}
	}
	binding, err := s.resolvePostgresBinding(ctx, projectID, execution.PostgresBindingName, execution.PostgresSecretVersionID)
	if err != nil {
		return result, err
	}
	s3, err := s.resolveS3Binding(ctx, projectID, execution.S3BindingName, execution.S3SecretVersionID)
	if err != nil {
		return result, err
	}
	if result.Operation.Phase == operations.Received {
		if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}); err != nil {
			return result, err
		}
	}
	next := database.ContainerExecution{
		ID: uuid.New(), ProjectID: projectID, OperationID: op.ID,
		Image: execution.Image, ImageDigest: execution.ImageDigest, Port: execution.Port,
		Hostname: execution.Hostname, PathPrefix: execution.PathPrefix,
		CPUMillis: execution.CPUMillis, MemoryBytes: execution.MemoryBytes,
		PIDs: execution.PIDs, OpenFiles: execution.OpenFiles, UserName: execution.UserName,
		Command: append([]string(nil), execution.Command...), Env: append([]string(nil), execution.Env...),
		PostgresBindingName: binding.name, PostgresSecretVersionID: binding.secretVersionID,
		S3BindingName: s3.name, S3SecretVersionID: s3.secretVersionID,
		HealthPath: execution.HealthPath, HealthTimeoutMS: execution.HealthTimeoutMS,
		VolumeName: execution.VolumeName, VolumeMountPath: execution.VolumeMountPath, VolumeDirectory: execution.VolumeDirectory,
	}
	container, execution, err = s.store.CreateContainerExecution(ctx, container.Name, next)
	if err != nil {
		return result, err
	}
	result.Container, result.Execution = container, execution
	reserved, activated, recorded := false, false, true
	defer func() {
		if err != nil && context.Cause(s.controlContext) != nil {
			err = errors.Join(err, context.Cause(s.controlContext))
		}
		if err == nil || activated {
			return
		}
		cause := err
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		cleaned := !reserved
		if reserved {
			cleanupErr := s.executions.Stop(cleanupCtx, executions.StopRequest{
				ExecutionID: result.Execution.ID, ProjectID: projectID,
				Ownership: s.ownership(result), ContainerID: result.Execution.DockerContainerID,
			})
			cleaned = cleanupErr == nil
			err = errors.Join(err, cleanupErr)
		}
		if recorded && cleaned {
			err = errors.Join(err, s.store.MarkContainerRestartFailed(cleanupCtx, projectID, container.ID, execution.ID, cause))
			result.Container.State, result.Container.Failure = "stopped", cause.Error()
			result.Execution.State, result.Execution.Failure = "failed", cause.Error()
		}
		if cleaned {
			err = errors.Join(err, s.advance(cleanupCtx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed}))
		}
	}()
	image, err := s.containers.PrepareImage(ctx, execution.Image)
	if err != nil {
		return result, err
	}
	if image.Digest != execution.ImageDigest {
		return result, fmt.Errorf("prepared image digest %s does not match stored digest %s: %w", image.Digest, execution.ImageDigest, containers.ErrImageDigestMismatch)
	}
	if err = s.reservations.EnsureProjectQuota(ctx, projectID, resources(s.configuration.ProjectQuota)); err != nil {
		return result, err
	}
	ownership := s.ownership(result)
	var dataVolume *containers.DataVolume
	if execution.VolumeName != "" {
		dataVolume = &containers.DataVolume{Name: execution.VolumeName, MountPath: execution.VolumeMountPath, Directory: execution.VolumeDirectory}
		found, findErr := s.containers.FindDataVolume(ctx, ownership, *dataVolume)
		if findErr != nil {
			return result, findErr
		}
		if !found {
			return result, fmt.Errorf("%w: %s", ErrDataVolumeMissing, execution.VolumeName)
		}
	}
	_, _, err = s.reservations.Reserve(ctx, capacity.Request{
		ExecutionID: result.Execution.ID, HostID: s.configuration.HostID, ProjectID: projectID,
		Resources: capacity.Resources{CPUMillis: execution.CPUMillis, MemoryBytes: execution.MemoryBytes, Instances: 1},
		Binding: capacity.Binding{
			Environment: ownership.Environment, Project: ownership.Project, Resource: ownership.Resource,
			Generation: ownership.Generation, Image: image.Reference,
		},
	})
	if err != nil {
		return result, err
	}
	reserved = true
	executionResult, err := s.executions.Start(ctx, executions.StartRequest{ExecutionID: result.Execution.ID, ProjectID: projectID, Spec: containers.Spec{
		Ownership: ownership, Image: image.Reference, User: execution.UserName,
		Command: execution.Command, Env: s3.environment(binding.environment(execution.Env)), Files: append(binding.files, s3.files...), Port: execution.Port,
		Access: containers.AccessPublished, DataVolume: dataVolume,
		Limits: containers.Limits{CPUMillis: execution.CPUMillis, MemoryBytes: execution.MemoryBytes, PIDs: execution.PIDs, OpenFiles: execution.OpenFiles},
	}})
	result.Execution.DockerContainerID = executionResult.Container.ID
	if result.Execution.DockerContainerID != "" {
		err = errors.Join(err, s.store.SetContainerExecutionDockerID(ctx, projectID, result.Execution.ID, result.Execution.DockerContainerID))
	}
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrRuntimeNotReady, err)
	}
	if dataVolume != nil && executionResult.Observation.WritableVolumes[dataVolume.MountPath] != dataVolume.Name {
		return result, fmt.Errorf("container data volume %s is not mounted at %s: %w", dataVolume.Name, dataVolume.MountPath, fs.ErrInvalid)
	}
	readiness, err := s.containers.WaitReady(ctx, ownership, executionResult.Container.ID, containers.ReadinessProbe{
		Port: execution.Port, Path: execution.HealthPath, Timeout: time.Duration(execution.HealthTimeoutMS) * time.Millisecond,
		Interval: healthInterval, Statuses: acceptedStatuses(),
	})
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrRuntimeNotReady, err)
	}
	target, err := publishedTarget(readiness.Observation, execution.Port)
	if err != nil {
		return result, err
	}
	if err := config.ValidateRoutes([]config.Route{{Host: execution.Hostname, Target: target}}); err != nil {
		return result, fmt.Errorf("validate container route target: %w: %w", fs.ErrInvalid, err)
	}
	if result.Operation.Phase != operations.Activating {
		if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Activating, Outcome: operations.InProgress}); err != nil {
			return result, err
		}
	}
	if _, err = s.store.ActivateContainer(ctx, result.Container, result.Execution, target); err != nil {
		checkCtx, checkCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer checkCancel()
		actual, _, checkErr := s.store.ContainerResult(checkCtx, projectID, result.Container.ID)
		if checkErr != nil || actual.State == "active" {
			activated = true
			err = errors.Join(err, checkErr)
		}
		return result, err
	}
	activated = true
	result.Container.State, result.Execution.State = "active", "running"
	result.Execution.Target = target
	err = s.finish(ctx, &result)
	return result, err
}

// Delete removes the public route and Docker container, then removes the
// logical container and all execution records. A repeated delete is idempotent.
func (s *Service) Delete(ctx context.Context, projectID, containerID uuid.UUID, key string) (err error) {
	if err := validateLifecycleRequest(projectID, containerID, key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	stopCancellation := context.AfterFunc(s.controlContext, cancel)
	defer stopCancellation()
	if err := s.Ready(ctx); err != nil {
		return fmt.Errorf("verify container controller ownership: %w", err)
	}
	op, fresh, err := s.registerLifecycle(ctx, projectID, containerID, "container.delete", key)
	if err != nil {
		return err
	}
	result := Result{Operation: op}
	if !fresh && op.Outcome == operations.Succeeded {
		return nil
	}
	if !fresh && op.Outcome == operations.Failed {
		return gorm.ErrRecordNotFound
	}
	container, execution, err := s.store.ContainerResult(ctx, projectID, containerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if !fresh {
				if op.Outcome == operations.InProgress && op.Phase != operations.Received {
					if _, routeErr := s.publisher.RemoveResource(ctx, projectID.String(), containerID.String()); routeErr != nil {
						return fmt.Errorf("remove container route: %w", routeErr)
					}
					return s.finishLifecycle(ctx, &result)
				}
				return gorm.ErrRecordNotFound
			}
			advanceErr := s.advance(ctx, &result, operations.Progress{Phase: result.Operation.Phase, Outcome: operations.Failed})
			return errors.Join(gorm.ErrRecordNotFound, advanceErr)
		}
		return err
	}
	result.Container, result.Execution = container, execution
	if container.State == "pending" {
		return database.ErrContainerPublicationPending
	}
	if result.Operation.Phase == operations.Received {
		if err = s.advance(ctx, &result, operations.Progress{Phase: operations.Preparing, Outcome: operations.InProgress}); err != nil {
			return err
		}
	}
	if container.State == "active" || execution.State == "running" {
		if err = s.stopContainer(ctx, &result); err != nil {
			return err
		}
	} else if _, err = s.publisher.RemoveResource(ctx, projectID.String(), containerID.String()); err != nil {
		return fmt.Errorf("remove container route: %w", err)
	}
	if result.Execution.VolumeName != "" {
		dataVolume := containers.DataVolume{
			Name: result.Execution.VolumeName, MountPath: result.Execution.VolumeMountPath, Directory: result.Execution.VolumeDirectory,
		}
		if err = s.containers.RemoveDataVolume(ctx, s.ownership(result), dataVolume); err != nil {
			return err
		}
	}
	if err = s.store.DeleteContainer(ctx, projectID, containerID); err != nil {
		return err
	}
	return s.finishLifecycle(ctx, &result)
}

func validateLifecycleRequest(projectID, containerID uuid.UUID, key string) error {
	if projectID == uuid.Nil || containerID == uuid.Nil || len(key) > 256 || strings.TrimSpace(key) == "" || strings.ContainsRune(key, 0) || !utf8.ValidString(key) {
		return fmt.Errorf("container project, container ID and Idempotency-Key are invalid: %w", fs.ErrInvalid)
	}
	return nil
}

func (s *Service) registerLifecycle(ctx context.Context, projectID, containerID uuid.UUID, action, key string) (operations.Operation, bool, error) {
	return s.operations.Register(ctx, operations.Registration{
		ProjectID: projectID, Action: action + ":" + containerID.String(),
		IdempotencyKey: key, RequestFingerprint: containerID.String(),
	})
}

func (s *Service) stopContainer(ctx context.Context, result *Result) error {
	if result.Execution.State == "pending" {
		return database.ErrContainerPublicationPending
	}
	if _, err := s.publisher.RemoveResource(ctx, result.Container.ProjectID.String(), result.Container.ID.String()); err != nil {
		return fmt.Errorf("remove container route: %w", err)
	}
	if result.Container.State == "active" || result.Execution.State == "running" {
		if err := s.executions.Stop(ctx, executions.StopRequest{
			ExecutionID: result.Execution.ID, ProjectID: result.Container.ProjectID,
			Ownership: s.ownership(*result), ContainerID: result.Execution.DockerContainerID,
		}); err != nil {
			return fmt.Errorf("%w: stop container execution %s: %w", ErrRuntimeStop, result.Execution.ID, err)
		}
	}
	if err := s.store.MarkContainerStopped(ctx, result.Container.ProjectID, result.Container.ID, result.Execution.ID); err != nil {
		return err
	}
	result.Container.State, result.Container.Failure = "stopped", ""
	result.Container.ActiveExecutionID = nil
	result.Execution.State, result.Execution.Failure = "stopped", ""
	result.Execution.Target = ""
	return nil
}

func (s *Service) finishLifecycle(ctx context.Context, result *Result) error {
	if result.Operation.Phase != operations.Finalizing {
		if err := s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.InProgress}); err != nil {
			return err
		}
	}
	return s.advance(ctx, result, operations.Progress{Phase: operations.Finalizing, Outcome: operations.Succeeded})
}

func dataVolumeMountPath(request Request) string {
	if request.DataVolume == nil {
		return ""
	}
	return request.DataVolume.MountPath
}

func dataVolumeDirectory(request Request) string {
	if request.DataVolume == nil {
		return ""
	}
	return request.DataVolume.Directory
}

func publishedTarget(observation containers.Observation, port uint16) (string, error) {
	for _, published := range observation.Ports {
		if published.ContainerPort == port && published.Protocol == "tcp" {
			return "http://" + published.HostAddress(), nil
		}
	}
	return "", fmt.Errorf("container port %d/tcp is not published: %w", port, containers.ErrNotReady)
}
