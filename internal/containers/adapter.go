package containers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Adapter manages containers and images on one Docker Engine. It is safe for
// concurrent use. The caller owns the Docker client and must close it after the
// adapter is no longer used. Every container operation other than Create
// inspects the container first and returns ErrNotOwned when its canonical name
// or ownership labels do not match.
// WaitReady probes HTTP readiness through the published loopback endpoint.
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

// Create creates a stopped container with ownership labels. AccessPublished
// containers publish the container port on the host loopback interface;
// AccessNetwork containers join the requested existing bridge network without
// publishing a host port. The actual endpoint is reported by Observe after
// Start. A duplicate container name returns ErrConflict, and a missing network,
// including a name that Docker resolves to a different network, returns
// ErrNetworkNotFound.
func (a *Adapter) Create(ctx context.Context, spec Spec) (Container, error) {
	options, err := spec.createOptions()
	if err != nil {
		return Container{}, err
	}
	if spec.Access == AccessNetwork {
		if err := a.checkNetwork(ctx, spec.Network); err != nil {
			return Container{}, fmt.Errorf("create container %s: %w", options.Name, err)
		}
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

// Find returns the managed container for the ownership's deterministic name.
// A missing container returns found=false without an error. A container with
// the same name but different ownership labels returns ErrNotOwned.
func (a *Adapter) Find(ctx context.Context, ownership Ownership) (Observation, bool, error) {
	if err := ownership.validate(); err != nil {
		return Observation{}, false, err
	}
	name := ownership.containerName()
	result, err := a.docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return Observation{}, false, nil
		}
		return Observation{}, false, fmt.Errorf("find container %s: %w", name, err)
	}
	if err := ownership.verify(result.Container); err != nil {
		return Observation{}, false, fmt.Errorf("find container %s: %w", name, err)
	}
	observation, err := observe(result.Container)
	if err != nil {
		return Observation{}, false, err
	}
	return observation, true, nil
}

// Start starts a container that belongs to the requested ownership. Starting a
// running container is a no-op. In network mode the requested network is
// checked again before starting, and a missing network returns
// ErrNetworkNotFound instead of ErrNotFound while the container remains in its
// previous state.
func (a *Adapter) Start(ctx context.Context, ownership Ownership, id string) (Observation, error) {
	info, err := a.inspectOwned(ctx, ownership, id)
	if err != nil {
		return Observation{}, err
	}
	if info.State == nil {
		return Observation{}, fmt.Errorf("start container %s: state is missing: %w", id, fs.ErrInvalid)
	}
	if !info.State.Running {
		networkName := managedNetworkMode(info)
		if networkName != "" {
			if err := a.checkNetwork(ctx, networkName); err != nil {
				return Observation{}, fmt.Errorf("start container %s: %w", id, err)
			}
		}
		if _, err := a.docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
			if networkName != "" && cerrdefs.IsNotFound(err) {
				return Observation{}, a.networkStartError(ctx, ownership, id, networkName, err)
			}
			return Observation{}, dockerOperationError("start", id, err)
		}
	}
	return a.Observe(ctx, ownership, id)
}

// networkStartError distinguishes a missing network from a missing container
// when ContainerStart reports a 404. The network can be removed after the
// pre-start check and before the engine starts the container.
func (a *Adapter) networkStartError(ctx context.Context, ownership Ownership, id, networkName string, startErr error) error {
	_, inspectErr := a.inspectOwned(ctx, ownership, id)
	switch {
	case inspectErr == nil:
		if err := a.checkNetwork(ctx, networkName); err != nil {
			if errors.Is(err, ErrNetworkNotFound) {
				return fmt.Errorf("start container %s: %w: %w", id, ErrNetworkNotFound, startErr)
			}
			return fmt.Errorf("start container %s: %w", id, err)
		}
		// The container and network still exist. Preserve the original error
		// instead of misreporting a missing container.
		return fmt.Errorf("start container %s: %w", id, startErr)
	case errors.Is(inspectErr, ErrNotFound):
		return dockerOperationError("start", id, startErr)
	default:
		return fmt.Errorf("verify container %s after start failure: %w", id, inspectErr)
	}
}

// managedNetworkMode returns the user-defined bridge network name configured
// for a container, or an empty string for Docker's special network modes.
func managedNetworkMode(info container.InspectResponse) string {
	if info.HostConfig == nil {
		return ""
	}
	mode := string(info.HostConfig.NetworkMode)
	switch mode {
	case "", network.NetworkDefault, network.NetworkBridge, network.NetworkHost, network.NetworkNone:
		return ""
	}
	if strings.HasPrefix(mode, "container:") {
		return ""
	}
	return mode
}

// Observe returns the current state and actual endpoints of a container that
// belongs to the requested ownership. Ports contains loopback bindings and
// Endpoints contains user-defined network endpoints. A missing container
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
		if !cerrdefs.IsConflict(err) || info.HostConfig == nil || !info.HostConfig.AutoRemove {
			return dockerOperationError("remove", id, err)
		}
		// Docker may already be removing an auto-remove container after exit.
		if waitErr := a.waitForRemoval(ctx, id); waitErr != nil {
			return fmt.Errorf("confirm automatic removal of container %s: %w", id, errors.Join(err, waitErr))
		}
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

func (a *Adapter) waitForRemoval(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, stopTimeout)
	defer cancel()
	wait := a.docker.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	select {
	case result := <-wait.Result:
		if result.Error != nil && result.Error.Message != "" {
			return errors.New(result.Error.Message)
		}
		return nil
	case err := <-wait.Error:
		if cerrdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
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
	pids := s.Limits.PIDs
	seconds := int(stopTimeout / time.Second)
	labels := s.Ownership.labels()
	labels[LabelPort] = strconv.FormatUint(uint64(s.Port), 10)
	options := client.ContainerCreateOptions{
		Name: s.Ownership.containerName(),
		Config: &container.Config{
			Image:        s.Image,
			User:         s.User,
			Cmd:          slices.Clone(s.Command),
			Env:          slices.Clone(s.Env),
			ExposedPorts: network.PortSet{port: {}},
			Labels:       labels,
			StopTimeout:  &seconds,
		},
		HostConfig: &container.HostConfig{
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
	}
	if s.ChromiumSeccompProfile != "" {
		init := true
		options.HostConfig.Init = &init
		options.HostConfig.AutoRemove = true
		options.HostConfig.CapDrop = []string{"ALL"}
		options.HostConfig.ShmSize = 256 << 20
		options.HostConfig.SecurityOpt = []string{"no-new-privileges", "seccomp=" + s.ChromiumSeccompProfile}
	}
	switch s.Access {
	case AccessPublished:
		options.HostConfig.NetworkMode = network.NetworkBridge
		options.HostConfig.PortBindings = network.PortMap{
			port: {{HostIP: netip.MustParseAddr(loopbackHost)}},
		}
	case AccessNetwork:
		options.HostConfig.NetworkMode = container.NetworkMode(s.Network)
		options.NetworkingConfig = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				s.Network: {Aliases: []string{s.Ownership.networkAlias()}},
			},
		}
	}
	return options, nil
}

// checkNetwork verifies that the requested network exists and can host a
// managed container. The network must be a local bridge network that is not a
// config-only placeholder, because managed network endpoints rely on Docker's
// embedded DNS for the stable alias. Network ownership and labels are not
// verified here; callers must not pass project-controlled names before #154
// provides network ownership.
func (a *Adapter) checkNetwork(ctx context.Context, name string) error {
	result, err := a.docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("inspect network %q: %w: %w", name, ErrNetworkNotFound, err)
		}
		return fmt.Errorf("inspect network %q: %w", name, err)
	}
	return verifyNetwork(name, result.Network)
}

func verifyNetwork(name string, info network.Inspect) error {
	if info.Name != name {
		return fmt.Errorf("inspect network %q: Docker resolved network name %q: %w", name, info.Name, ErrNetworkNotFound)
	}
	if info.Scope != "local" {
		return fmt.Errorf("inspect network %q: scope is %q, want local: %w", name, info.Scope, fs.ErrInvalid)
	}
	if info.Driver != "bridge" {
		return fmt.Errorf("inspect network %q: driver is %q, want bridge: %w", name, info.Driver, fs.ErrInvalid)
	}
	if info.ConfigOnly {
		return fmt.Errorf("inspect network %q: config-only networks cannot host containers: %w", name, fs.ErrInvalid)
	}
	return nil
}
