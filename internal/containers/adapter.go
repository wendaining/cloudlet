package containers

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Adapter manages containers on one Docker Engine. It is safe for concurrent
// use. The caller owns the Docker client and must close it after the adapter is
// no longer used. Every operation other than Create inspects the container
// first and returns ErrNotOwned when its canonical name or ownership labels do
// not match. WaitReady additionally probes HTTP readiness through the actual
// published loopback endpoint.
type Adapter struct {
	docker     *client.Client
	httpClient *http.Client
}

// New verifies that the Docker Engine is reachable and provides Linux
// containers.
func New(ctx context.Context, docker *client.Client) (*Adapter, error) {
	if docker == nil {
		return nil, fmt.Errorf("create container adapter: Docker client is required: %w", fs.ErrInvalid)
	}
	ping, err := docker.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return nil, fmt.Errorf("create container adapter: connect Docker Engine: %w", err)
	}
	if ping.OSType != "linux" {
		return nil, fmt.Errorf("create container adapter: Docker Engine provides %q containers, want linux: %w", ping.OSType, fs.ErrInvalid)
	}
	return &Adapter{docker: docker, httpClient: newProbeClient()}, nil
}

// Create creates a stopped container with ownership labels and a loopback port
// binding. The actual host port is assigned by Docker and reported by Observe
// after Start. A duplicate container name returns ErrConflict.
func (a *Adapter) Create(ctx context.Context, spec Spec) (Container, error) {
	options, err := spec.createOptions()
	if err != nil {
		return Container{}, err
	}
	result, err := a.docker.ContainerCreate(ctx, options)
	if err != nil {
		if cerrdefs.IsConflict(err) {
			return Container{}, fmt.Errorf("create container %s: %w: %w", options.Name, ErrConflict, err)
		}
		return Container{}, fmt.Errorf("create container %s: %w", options.Name, err)
	}
	if err := validateContainerID(result.ID); err != nil {
		return Container{}, fmt.Errorf("create container %s: %w", options.Name, err)
	}
	return Container{ID: result.ID, Name: options.Name}, nil
}

// Start starts a container that belongs to the requested ownership. Starting a
// running container is a no-op.
func (a *Adapter) Start(ctx context.Context, ownership Ownership, id string) (Observation, error) {
	info, err := a.inspectOwned(ctx, ownership, id)
	if err != nil {
		return Observation{}, err
	}
	if info.State == nil {
		return Observation{}, fmt.Errorf("start container %s: state is missing: %w", id, fs.ErrInvalid)
	}
	if !info.State.Running {
		if _, err := a.docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
			return Observation{}, dockerOperationError("start", id, err)
		}
	}
	return a.Observe(ctx, ownership, id)
}

// Observe returns the current state and actual loopback port bindings of a
// container that belongs to the requested ownership. A missing container
// returns ErrNotFound.
func (a *Adapter) Observe(ctx context.Context, ownership Ownership, id string) (Observation, error) {
	info, err := a.inspectOwned(ctx, ownership, id)
	if err != nil {
		return Observation{}, err
	}
	return observe(info)
}

// Stop stops a container that belongs to the requested ownership. Stopping an
// already stopped container is a no-op. A missing container returns
// ErrNotFound.
func (a *Adapter) Stop(ctx context.Context, ownership Ownership, id string) error {
	info, err := a.inspectOwned(ctx, ownership, id)
	if err != nil {
		return err
	}
	if info.State == nil {
		return fmt.Errorf("stop container %s: state is missing: %w", id, fs.ErrInvalid)
	}
	if info.State.Running {
		seconds := int(stopTimeout / time.Second)
		if _, err := a.docker.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
			return dockerOperationError("stop", id, err)
		}
	}
	observation, err := a.Observe(ctx, ownership, id)
	if err != nil {
		return err
	}
	if observation.Running {
		return fmt.Errorf("stop container %s: still running after stop: %w", id, fs.ErrInvalid)
	}
	return nil
}

// Remove removes a stopped container that belongs to the requested ownership.
// Volumes are managed separately and are not removed. A running container is
// rejected, and a missing container returns ErrNotFound.
func (a *Adapter) Remove(ctx context.Context, ownership Ownership, id string) error {
	info, err := a.inspectOwned(ctx, ownership, id)
	if err != nil {
		return err
	}
	if info.State == nil {
		return fmt.Errorf("remove container %s: state is missing: %w", id, fs.ErrInvalid)
	}
	if info.State.Running {
		return fmt.Errorf("remove container %s: container is running, stop it first: %w", id, fs.ErrInvalid)
	}
	if _, err := a.docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{RemoveVolumes: false}); err != nil {
		return dockerOperationError("remove", id, err)
	}
	// Confirm removal so callers can rely on the container and its ports being
	// released.
	if _, err := a.docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); err == nil {
		return fmt.Errorf("remove container %s: container still exists: %w", id, fs.ErrInvalid)
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("verify removal of container %s: %w", id, err)
	}
	return nil
}

func (a *Adapter) inspectOwned(ctx context.Context, ownership Ownership, id string) (container.InspectResponse, error) {
	if err := ownership.validate(); err != nil {
		return container.InspectResponse{}, err
	}
	if err := validateContainerID(id); err != nil {
		return container.InspectResponse{}, err
	}
	result, err := a.docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w: %w", id, ErrNotFound, err)
		}
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	if err := ownership.verify(result.Container); err != nil {
		return container.InspectResponse{}, fmt.Errorf("inspect container %s: %w", id, err)
	}
	return result.Container, nil
}

func dockerOperationError(operation, id string, err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%s container %s: %w: %w", operation, id, ErrNotFound, err)
	}
	return fmt.Errorf("%s container %s: %w", operation, id, err)
}

func validateContainerID(id string) error {
	if len(id) < 12 || len(id) > 64 {
		return fmt.Errorf("validate container ID %q: must contain 12 to 64 hexadecimal characters: %w", id, fs.ErrInvalid)
	}
	for index := 0; index < len(id); index++ {
		char := id[index]
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f' || char >= 'A' && char <= 'F') {
			return fmt.Errorf("validate container ID %q: must contain only hexadecimal characters: %w", id, fs.ErrInvalid)
		}
	}
	return nil
}

func (s Spec) createOptions() (client.ContainerCreateOptions, error) {
	if err := s.validate(); err != nil {
		return client.ContainerCreateOptions{}, err
	}
	port, err := network.ParsePort(strconv.FormatUint(uint64(s.Port), 10) + "/tcp")
	if err != nil {
		return client.ContainerCreateOptions{}, fmt.Errorf("build container spec: parse port %d: %w", s.Port, err)
	}
	hostIP := netip.MustParseAddr(loopbackHost)
	pids := s.Limits.PIDs
	seconds := int(stopTimeout / time.Second)
	return client.ContainerCreateOptions{
		Name: s.Ownership.containerName(),
		Config: &container.Config{
			Image:        s.Image,
			User:         s.User,
			Cmd:          slices.Clone(s.Command),
			Env:          slices.Clone(s.Env),
			ExposedPorts: network.PortSet{port: {}},
			Labels:       s.Ownership.labels(),
			StopTimeout:  &seconds,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:  network.NetworkBridge,
			PortBindings: network.PortMap{port: {{HostIP: hostIP}}},
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyDisabled,
			},
			Resources: container.Resources{
				NanoCPUs:  s.Limits.CPUMillis * 1_000_000,
				Memory:    s.Limits.MemoryBytes,
				PidsLimit: &pids,
				Ulimits: []*container.Ulimit{{
					Name: "nofile",
					Soft: s.Limits.OpenFiles,
					Hard: s.Limits.OpenFiles,
				}},
			},
		},
	}, nil
}
