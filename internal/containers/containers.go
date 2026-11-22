package containers

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/moby/moby/api/types/container"
)

// Ownership labels identify the platform scope of a managed container. The
// environment label uses the same value as CLOUDLET_INSTANCE.
const (
	LabelManaged     = "io.cloudlet.managed"
	LabelEnvironment = "io.cloudlet.instance"
	LabelComponent   = "io.cloudlet.component"
	LabelProject     = "io.cloudlet.project"
	LabelResource    = "io.cloudlet.resource"
	LabelGeneration  = "io.cloudlet.generation"

	ComponentRuntime = "runtime"
)

const (
	loopbackHost        = "127.0.0.1"
	maxIdentifierLength = 48
	maxContainerName    = 200
	maxImageLength      = 255
	stopTimeout         = 10 * time.Second
)

var (
	// ErrNotOwned reports that a container does not carry the requested
	// ownership labels.
	ErrNotOwned = errors.New("container ownership mismatch")
	// ErrNotFound reports that a container does not exist.
	ErrNotFound = errors.New("container not found")
	// ErrConflict reports that a container with the requested name already
	// exists.
	ErrConflict = errors.New("container already exists")
)

// Ownership is the platform scope that owns a managed container. Environment
// uses the CLOUDLET_INSTANCE value, while Project, Resource and Generation
// identify the resource execution. Each identifier is limited to 48 bytes.
type Ownership struct {
	Environment string
	Project     string
	Resource    string
	Generation  uint64
}

// Limits are the resource limits applied to a managed container. Every field
// is required so the adapter never falls back to unlimited defaults.
type Limits struct {
	CPUMillis   int64
	MemoryBytes int64
	PIDs        int64
	OpenFiles   int64
}

// Spec describes the container to create. Image is the image reference, User
// is the non-root user, Port is the container TCP port published on loopback,
// Env holds process variables in KEY=value form, Command overrides the image
// command, and Limits are the required resource limits.
type Spec struct {
	Ownership Ownership
	Image     string
	User      string
	Command   []string
	Env       []string
	Port      uint16
	Limits    Limits
}

// Container identifies a container created by the adapter.
type Container struct {
	ID   string
	Name string
}

// PublishedPort is an actual loopback binding reported by Docker.
type PublishedPort struct {
	ContainerPort uint16
	Protocol      string
	HostIP        netip.Addr
	HostPort      uint16
}

// HostAddress returns the binding in host:port form.
func (p PublishedPort) HostAddress() string {
	return net.JoinHostPort(p.HostIP.String(), strconv.Itoa(int(p.HostPort)))
}

// Observation is the inspected state of a managed container. Ports contains
// only loopback bindings reported by Docker.
type Observation struct {
	ID       string
	Name     string
	Image    string
	Status   string
	Running  bool
	ExitCode int
	Ports    []PublishedPort
}

func (o Ownership) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"environment", o.Environment},
		{"project", o.Project},
		{"resource", o.Resource},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	if o.Generation == 0 {
		return fmt.Errorf("validate ownership: generation must be positive: %w", fs.ErrInvalid)
	}
	if name := o.containerName(); len(name) > maxContainerName {
		return fmt.Errorf("validate ownership: container name %q exceeds %d bytes: %w", name, maxContainerName, fs.ErrInvalid)
	}
	return nil
}

func validateIdentifier(field, value string) error {
	if value == "" {
		return fmt.Errorf("validate ownership: %s is required: %w", field, fs.ErrInvalid)
	}
	if len(value) > maxIdentifierLength {
		return fmt.Errorf("validate ownership: %s exceeds %d bytes: %w", field, maxIdentifierLength, fs.ErrInvalid)
	}
	if !isLetterOrDigit(value[0]) {
		return fmt.Errorf("validate ownership: %s must start with a letter or digit: %w", field, fs.ErrInvalid)
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if !isLetterOrDigit(char) && char != '.' && char != '_' && char != '-' {
			return fmt.Errorf("validate ownership: %s contains invalid character %q: %w", field, char, fs.ErrInvalid)
		}
	}
	return nil
}

func isLetterOrDigit(char byte) bool {
	return char >= 'a' && char <= 'z' ||
		char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9'
}

func (o Ownership) containerName() string {
	// Length prefixes keep identifiers containing hyphens unambiguous.
	return fmt.Sprintf("cloudlet-%d-%s-%d-%s-%d-%s-%d",
		len(o.Environment), o.Environment,
		len(o.Project), o.Project,
		len(o.Resource), o.Resource,
		o.Generation)
}

func (o Ownership) labels() map[string]string {
	return map[string]string{
		LabelManaged:     "true",
		LabelEnvironment: o.Environment,
		LabelComponent:   ComponentRuntime,
		LabelProject:     o.Project,
		LabelResource:    o.Resource,
		LabelGeneration:  strconv.FormatUint(o.Generation, 10),
	}
}

func (o Ownership) verify(info container.InspectResponse) error {
	if info.Config == nil {
		return fmt.Errorf("verify container ownership: configuration is missing: %w", fs.ErrInvalid)
	}
	if err := o.verifyLabels(info.Config.Labels); err != nil {
		return err
	}
	actualName := strings.TrimPrefix(info.Name, "/")
	expectedName := o.containerName()
	if actualName != expectedName {
		return fmt.Errorf("%w: container name is %q, want %q", ErrNotOwned, actualName, expectedName)
	}
	return nil
}

func (o Ownership) verifyLabels(labels map[string]string) error {
	for key, want := range o.labels() {
		got, found := labels[key]
		if !found || got != want {
			return fmt.Errorf("%w: label %s is %q, want %q", ErrNotOwned, key, got, want)
		}
	}
	return nil
}

func (s Spec) validate() error {
	if err := s.Ownership.validate(); err != nil {
		return err
	}
	if err := validateImage(s.Image); err != nil {
		return err
	}
	if err := validateUser(s.User); err != nil {
		return err
	}
	if s.Port == 0 {
		return fmt.Errorf("validate container spec: port must be between 1 and 65535: %w", fs.ErrInvalid)
	}
	if err := s.Limits.validate(); err != nil {
		return err
	}
	for _, argument := range s.Command {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("validate container spec: command contains a NUL byte: %w", fs.ErrInvalid)
		}
	}
	seen := make(map[string]struct{}, len(s.Env))
	for _, entry := range s.Env {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			return fmt.Errorf("validate container spec: environment entry %q must be KEY=value: %w", entry, fs.ErrInvalid)
		}
		if !validEnvironmentKey(key) {
			return fmt.Errorf("validate container spec: environment key %q is invalid: %w", key, fs.ErrInvalid)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("validate container spec: environment %q contains a NUL byte: %w", key, fs.ErrInvalid)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("validate container spec: duplicate environment key %q: %w", key, fs.ErrInvalid)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateImage(image string) error {
	if image == "" {
		return fmt.Errorf("validate container spec: image is required: %w", fs.ErrInvalid)
	}
	if len(image) > maxImageLength {
		return fmt.Errorf("validate container spec: image exceeds %d bytes: %w", maxImageLength, fs.ErrInvalid)
	}
	if strings.IndexFunc(image, func(char rune) bool {
		return unicode.IsSpace(char) || char == 0
	}) >= 0 {
		return fmt.Errorf("validate container spec: image %q contains whitespace or NUL: %w", image, fs.ErrInvalid)
	}
	return nil
}

func validateUser(user string) error {
	if user == "" {
		return fmt.Errorf("validate container spec: user is required: %w", fs.ErrInvalid)
	}
	if strings.IndexFunc(user, func(char rune) bool {
		return unicode.IsSpace(char) || char == 0
	}) >= 0 {
		return fmt.Errorf("validate container spec: user %q contains whitespace or NUL: %w", user, fs.ErrInvalid)
	}
	parts := strings.SplitN(user, ":", 2)
	if isRootIdentity(parts[0]) {
		return fmt.Errorf("validate container spec: user must not run as root: %w", fs.ErrInvalid)
	}
	if len(parts) == 2 && parts[1] != "" && isRootIdentity(parts[1]) {
		return fmt.Errorf("validate container spec: group must not be root: %w", fs.ErrInvalid)
	}
	return nil
}

func isRootIdentity(value string) bool {
	if value == "root" {
		return true
	}
	number, err := strconv.ParseUint(value, 10, 32)
	return err == nil && number == 0
}

func (l Limits) validate() error {
	if l.CPUMillis <= 0 || l.CPUMillis > 1_000_000 {
		return fmt.Errorf("validate container limits: CPU milliseconds must be between 1 and 1000000: %w", fs.ErrInvalid)
	}
	if l.MemoryBytes <= 0 {
		return fmt.Errorf("validate container limits: memory bytes must be positive: %w", fs.ErrInvalid)
	}
	if l.PIDs <= 0 {
		return fmt.Errorf("validate container limits: process limit must be positive: %w", fs.ErrInvalid)
	}
	if l.OpenFiles <= 0 {
		return fmt.Errorf("validate container limits: open file limit must be positive: %w", fs.ErrInvalid)
	}
	return nil
}

func validEnvironmentKey(key string) bool {
	if key == "" {
		return false
	}
	for index := 0; index < len(key); index++ {
		char := key[index]
		if index == 0 {
			if char != '_' && !isLetter(char) {
				return false
			}
			continue
		}
		if char != '_' && !isLetter(char) && (char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func isLetter(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
}

func observe(info container.InspectResponse) (Observation, error) {
	if info.State == nil {
		return Observation{}, fmt.Errorf("observe container %s: state is missing: %w", info.ID, fs.ErrInvalid)
	}
	observation := Observation{
		ID:       info.ID,
		Name:     strings.TrimPrefix(info.Name, "/"),
		Status:   string(info.State.Status),
		Running:  info.State.Running,
		ExitCode: info.State.ExitCode,
	}
	if info.Config != nil {
		observation.Image = info.Config.Image
	}
	if observation.Image == "" {
		observation.Image = info.Image
	}
	ports, err := publishedPorts(info.NetworkSettings)
	if err != nil {
		return Observation{}, fmt.Errorf("observe container %s: %w", info.ID, err)
	}
	observation.Ports = ports
	return observation, nil
}

func publishedPorts(settings *container.NetworkSettings) ([]PublishedPort, error) {
	if settings == nil {
		return nil, nil
	}
	var ports []PublishedPort
	for port, bindings := range settings.Ports {
		for _, binding := range bindings {
			if !binding.HostIP.IsValid() {
				return nil, fmt.Errorf("published port %s has no host address: %w", port, fs.ErrInvalid)
			}
			if !binding.HostIP.IsLoopback() {
				return nil, fmt.Errorf("published port %s is bound to non-loopback address %s: %w", port, binding.HostIP, fs.ErrInvalid)
			}
			hostPort, err := strconv.ParseUint(binding.HostPort, 10, 16)
			if err != nil || hostPort == 0 {
				return nil, fmt.Errorf("published port %s has invalid host port %q: %w", port, binding.HostPort, fs.ErrInvalid)
			}
			ports = append(ports, PublishedPort{
				ContainerPort: port.Num(),
				Protocol:      string(port.Proto()),
				HostIP:        binding.HostIP,
				HostPort:      uint16(hostPort),
			})
		}
	}
	slices.SortFunc(ports, func(left, right PublishedPort) int {
		if order := cmp.Compare(left.ContainerPort, right.ContainerPort); order != 0 {
			return order
		}
		if order := strings.Compare(left.Protocol, right.Protocol); order != 0 {
			return order
		}
		return cmp.Compare(left.HostPort, right.HostPort)
	})
	return ports, nil
}
