package containers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// NetworkResource is the resource label value for a project network. A project
// has one network, so the project label already provides the unique identity.
const NetworkResource = "network"

// NetworkSpec identifies the platform-managed Docker network of one project.
// Environment uses the CLOUDLET_INSTANCE value and Project is the stable
// project identifier. Each identifier is limited to 48 bytes.
type NetworkSpec struct {
	Environment string
	Project     string
}

// Network identifies a platform-managed Docker network.
type Network struct {
	ID   string
	Name string
}

// Validate reports whether the spec can identify a project network.
func (s NetworkSpec) Validate() error {
	return s.validate()
}

// Name returns the deterministic Docker network name. Call Validate before
// relying on the name.
func (s NetworkSpec) Name() string {
	return s.name()
}

func (s NetworkSpec) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"environment", s.Environment},
		{"project", s.Project},
	} {
		if err := validateIdentifier("network spec", field.name, field.value); err != nil {
			return err
		}
	}
	if name := s.name(); len(name) > maxNetworkName {
		return fmt.Errorf("validate network spec: network name %q exceeds %d bytes: %w", name, maxNetworkName, fs.ErrInvalid)
	}
	return nil
}

func (s NetworkSpec) name() string {
	// Length prefixes keep identifiers containing hyphens unambiguous.
	return fmt.Sprintf("cloudlet-net-%d-%s-%d-%s", len(s.Environment), s.Environment, len(s.Project), s.Project)
}

// labelPairs returns the ownership labels in a stable order so verification
// errors name the same label for the same input.
func (s NetworkSpec) labelPairs() [][2]string {
	return [][2]string{
		{LabelManaged, "true"},
		{LabelEnvironment, s.Environment},
		{LabelComponent, ComponentRuntime},
		{LabelProject, s.Project},
		{LabelResource, NetworkResource},
	}
}

func (s NetworkSpec) labels() map[string]string {
	pairs := s.labelPairs()
	labels := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		labels[pair[0]] = pair[1]
	}
	return labels
}

// networkSpec returns the network identity for a container ownership. The
// network is scoped to the environment and project, not to one execution.
func networkSpec(ownership Ownership) NetworkSpec {
	return NetworkSpec{Environment: ownership.Environment, Project: ownership.Project}
}

// EnsureNetwork returns the project network, creating it when it does not
// exist. The network is a user-defined bridge network with ownership labels.
// An existing network with the canonical name but different ownership, or a
// managed network of the same project under a different name, returns an error
// instead of being adopted or duplicated. Concurrent calls are idempotent: a
// create conflict is resolved by inspecting the existing network again.
func (a *Adapter) EnsureNetwork(ctx context.Context, spec NetworkSpec) (Network, error) {
	if err := spec.validate(); err != nil {
		return Network{}, err
	}
	existing, found, err := a.FindNetwork(ctx, spec)
	if err != nil {
		return Network{}, err
	}
	if found {
		return existing, nil
	}
	// Refuse to create a second network for a project whose managed network
	// already exists under another name. This keeps one network per project
	// and surfaces stale or manually created resources instead of hiding them.
	others, err := a.listProjectNetworks(ctx, spec)
	if err != nil {
		return Network{}, err
	}
	for _, other := range others {
		if other.Name != spec.name() {
			return Network{}, fmt.Errorf("ensure network %q: project %q already has managed network %q: %w",
				spec.name(), spec.Project, other.Name, ErrConflict)
		}
	}
	result, err := a.docker.NetworkCreate(ctx, spec.name(), client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: spec.labels(),
	})
	if err != nil {
		if cerrdefs.IsConflict(err) {
			existing, found, findErr := a.FindNetwork(ctx, spec)
			switch {
			case findErr != nil:
				return Network{}, fmt.Errorf("create network %q: %w", spec.name(), errors.Join(ErrConflict, err, findErr))
			case found:
				return existing, nil
			default:
				return Network{}, fmt.Errorf("create network %q: %w: %w", spec.name(), ErrConflict, err)
			}
		}
		return Network{}, fmt.Errorf("create network %q: %w", spec.name(), err)
	}
	if err := validateNetworkID(result.ID); err != nil {
		return Network{}, fmt.Errorf("create network %q: %w", spec.name(), err)
	}
	// Read the created network back so callers only receive a network whose
	// shape and labels were verified by Docker, not just the create response.
	info, err := a.docker.NetworkInspect(ctx, result.ID, client.NetworkInspectOptions{})
	if err != nil {
		return Network{}, fmt.Errorf("verify created network %q: %w", spec.name(), err)
	}
	if err := verifyProjectNetwork(spec.name(), info.Network.Network, spec); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, removeErr := a.docker.NetworkRemove(cleanupCtx, result.ID, client.NetworkRemoveOptions{})
		return Network{}, fmt.Errorf("verify created network %q: %w", spec.name(), errors.Join(err, removeErr))
	}
	return networkFromInspect(info.Network)
}

// FindNetwork returns the project network when it exists. A missing network
// returns found=false without an error. A network with the canonical name but
// missing or different ownership labels returns ErrNetworkNotOwned.
func (a *Adapter) FindNetwork(ctx context.Context, spec NetworkSpec) (Network, bool, error) {
	if err := spec.validate(); err != nil {
		return Network{}, false, err
	}
	result, err := a.docker.NetworkInspect(ctx, spec.name(), client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return Network{}, false, nil
		}
		return Network{}, false, fmt.Errorf("find network %q: %w", spec.name(), err)
	}
	if err := verifyProjectNetwork(spec.name(), result.Network.Network, spec); err != nil {
		return Network{}, false, fmt.Errorf("find network %q: %w", spec.name(), err)
	}
	network, err := networkFromInspect(result.Network)
	if err != nil {
		return Network{}, false, fmt.Errorf("find network %q: %w", spec.name(), err)
	}
	return network, true, nil
}

// RemoveNetwork removes the project network after verifying its ownership and
// that no container is attached. A missing network returns ErrNetworkNotFound,
// an unowned network returns ErrNetworkNotOwned, and attached containers
// return ErrNetworkInUse without removing the network.
func (a *Adapter) RemoveNetwork(ctx context.Context, spec NetworkSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	result, err := a.docker.NetworkInspect(ctx, spec.name(), client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove network %q: %w: %w", spec.name(), ErrNetworkNotFound, err)
		}
		return fmt.Errorf("remove network %q: %w", spec.name(), err)
	}
	info := result.Network
	if err := verifyProjectNetwork(spec.name(), info.Network, spec); err != nil {
		return fmt.Errorf("remove network %q: %w", spec.name(), err)
	}
	// Docker reports only running endpoints in Inspect.Containers, while a
	// stopped container keeps its network reference and cannot start after the
	// network is removed. List all containers so cleanup waits until stopped
	// executions are removed as well.
	attached, err := a.networkContainers(ctx, info.ID)
	if err != nil {
		return fmt.Errorf("remove network %q: %w", spec.name(), err)
	}
	if len(attached) > 0 {
		return fmt.Errorf("remove network %q: %d container(s) still attached (%s): %w",
			spec.name(), len(attached), strings.Join(attached, ", "), ErrNetworkInUse)
	}
	if _, err := a.docker.NetworkRemove(ctx, info.ID, client.NetworkRemoveOptions{}); err != nil {
		if cerrdefs.IsConflict(err) {
			return fmt.Errorf("remove network %q: %w: %w", spec.name(), ErrNetworkInUse, err)
		}
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("remove network %q: %w: %w", spec.name(), ErrNetworkNotFound, err)
		}
		return fmt.Errorf("remove network %q: %w", spec.name(), err)
	}
	// Confirm removal so callers can rely on the network name being released.
	if _, err := a.docker.NetworkInspect(ctx, info.ID, client.NetworkInspectOptions{}); err == nil {
		return fmt.Errorf("remove network %q: network still exists: %w", spec.name(), fs.ErrInvalid)
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("verify removal of network %q: %w", spec.name(), err)
	}
	return nil
}

// listProjectNetworks returns managed networks carrying the project label. The
// Docker label filter is deliberately broad; ownership labels are verified in
// Go so an unexpected label combination cannot select another resource.
func (a *Adapter) listProjectNetworks(ctx context.Context, spec NetworkSpec) ([]network.Summary, error) {
	filters := make(client.Filters).Add("label", LabelProject+"="+spec.Project)
	result, err := a.docker.NetworkList(ctx, client.NetworkListOptions{Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list networks for project %q: %w", spec.Project, err)
	}
	var networks []network.Summary
	for _, item := range result.Items {
		if item.Labels[LabelManaged] != "true" ||
			item.Labels[LabelEnvironment] != spec.Environment ||
			item.Labels[LabelComponent] != ComponentRuntime ||
			item.Labels[LabelResource] != NetworkResource {
			continue
		}
		networks = append(networks, item)
	}
	slices.SortFunc(networks, func(left, right network.Summary) int {
		return strings.Compare(left.Name, right.Name)
	})
	return networks, nil
}

func networkFromInspect(info network.Inspect) (Network, error) {
	if err := validateNetworkID(info.ID); err != nil {
		return Network{}, err
	}
	if info.Name == "" {
		return Network{}, fmt.Errorf("network has no name: %w", fs.ErrInvalid)
	}
	return Network{ID: info.ID, Name: info.Name}, nil
}

// networkContainers returns the names of all containers, including stopped
// ones, that still reference the network.
func (a *Adapter) networkContainers(ctx context.Context, networkID string) ([]string, error) {
	filters := make(client.Filters).Add("network", networkID)
	result, err := a.docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list containers on network %q: %w", networkID, err)
	}
	names := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		name := item.ID
		if len(item.Names) > 0 {
			name = strings.TrimPrefix(item.Names[0], "/")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

// verifyProjectNetwork checks the network shape and the project ownership
// labels. The canonical name check prevents a container from joining a
// different managed network of the same project.
func verifyProjectNetwork(name string, info network.Network, spec NetworkSpec) error {
	if err := verifyNetwork(name, info); err != nil {
		return err
	}
	if info.Name != spec.name() {
		return fmt.Errorf("network %q is not the project network %q: %w", info.Name, spec.name(), ErrNetworkNotOwned)
	}
	for _, pair := range spec.labelPairs() {
		got, found := info.Labels[pair[0]]
		if !found || got != pair[1] {
			return fmt.Errorf("label %s is %q, want %q: %w", pair[0], got, pair[1], ErrNetworkNotOwned)
		}
	}
	return nil
}
