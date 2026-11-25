package containers

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/moby/moby/api/types/network"
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
	// LabelPort records the managed container port. Docker merges image
	// exposed ports into the container configuration, so this label selects
	// the port that the adapter observes as the managed endpoint.
	LabelPort = "io.cloudlet.port"

	ComponentRuntime = "runtime"
)

const (
	loopbackHost        = "127.0.0.1"
	maxIdentifierLength = 48
	maxContainerName    = 200
	maxNetworkName      = 255
	// A repository name may use 255 bytes and a digest adds the algorithm and
	// encoded value, so the limit covers a fully qualified reference.
	maxImageLength = 512
	stopTimeout    = 10 * time.Second
)

// AccessMode selects how consumers reach a managed container.
type AccessMode string

const (
	// AccessPublished publishes the container port on the host loopback
	// interface. It is used by local development.
	AccessPublished AccessMode = "published"
	// AccessNetwork attaches the container to an existing user-defined bridge
	// network. The container port is reachable only from peers on that
	// network.
	AccessNetwork AccessMode = "network"
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
	// ErrNetworkNotFound reports that the requested Docker network does not
	// exist.
	ErrNetworkNotFound = errors.New("container network not found")
	// ErrImagePull reports that Docker Engine could not pull the requested
	// image. The underlying registry or transport error remains available
	// through errors.Is and errors.As.
	ErrImagePull = errors.New("image pull failed")
	// ErrImageDigestMismatch reports that the local image does not report the
	// requested immutable digest.
	ErrImageDigestMismatch = errors.New("image digest mismatch")
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

// Spec describes the container to create. Image is the image reference,
// normally the Reference returned by PrepareImage so the container uses an
// immutable digest, User is the non-root user, Port is the container TCP port,
// Env holds process variables in KEY=value form, Command overrides the image
// command, and Limits are the required resource limits.
//
// Access is required. AccessPublished publishes Port on the host loopback
// interface. AccessNetwork joins the existing user-defined bridge network in
// Network and exposes Port only to peers on that network.
//
// The adapter does not verify network ownership. Callers must pass a
// platform-controlled network name; project network isolation and ownership
// labels are handled by #154.
type Spec struct {
	Ownership  Ownership
	Image      string
	User       string
	Command    []string
	Env        []string
	Port       uint16
	Access     AccessMode
	Network    string
	Limits     Limits
	Files      []File
	DataVolume *DataVolume

	// ChromiumSeccompProfile enables the hardened Chromium container settings.
	// It must come from platform configuration, not a management API request.
	ChromiumSeccompProfile string
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

// NetworkEndpoint is an actual container endpoint on a user-defined bridge
// network. Name is the managed DNS name registered on the network, or empty
// when Docker did not register it; IP is the address assigned by Docker, and
// ContainerPort is the managed container port.
type NetworkEndpoint struct {
	Network       string
	Name          string
	IP            netip.Addr
	ContainerPort uint16
	Protocol      string
}

// Address returns the connection address in name:port form. When the endpoint
// has no managed DNS name, it uses the observed IP address.
func (e NetworkEndpoint) Address() string {
	host := e.Name
	if host == "" {
		host = e.IP.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(int(e.ContainerPort)))
}

// IPAddress returns the observed IP address and container port.
func (e NetworkEndpoint) IPAddress() string {
	return net.JoinHostPort(e.IP.String(), strconv.Itoa(int(e.ContainerPort)))
}

// Observation is the inspected state of a managed container. Ports contains
// only loopback bindings reported by Docker. CPUMillis, MemoryBytes, PIDs and
// OpenFiles are the enforced limits reported by Docker. Endpoints contains
// endpoints on user-defined bridge networks.
type Observation struct {
	ID              string
	Name            string
	Image           string
	Status          string
	Running         bool
	ExitCode        int
	CPUMillis       int64
	MemoryBytes     int64
	PIDs            int64
	OpenFiles       int64
	Ports           []PublishedPort
	FilesDigest     string
	Endpoints       []NetworkEndpoint
	WritableVolumes map[string]string
}

// Validate reports whether the ownership can identify a managed container.
func (o Ownership) Validate() error {
	return o.validate()
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

// networkAlias returns a stable DNS label for the ownership. The canonical
// container name can exceed the 63-byte DNS label limit, so network peers use
// this alias instead.
func (o Ownership) networkAlias() string {
	return managedNetworkAlias(o.containerName())
}

func managedNetworkAlias(containerName string) string {
	sum := sha256.Sum256([]byte(containerName))
	return "cloudlet-" + hex.EncodeToString(sum[:16])
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

// Validate reports whether the spec can be passed to Create.
func (s Spec) Validate() error {
	return s.validate()
}

func (s Spec) validate() error {
	if _, err := filesDigest(s.Files); err != nil {
		return err
	}
	if err := s.Ownership.validate(); err != nil {
		return err
	}
	if err := validateImage(s.Image); err != nil {
		return err
	}
	if err := validateUser(s.User); err != nil {
		return err
	}
	if s.DataVolume != nil {
		if err := s.DataVolume.validate(s.User); err != nil {
			return err
		}
	}
	if err := s.validateAccess(); err != nil {
		return err
	}
	if s.Port == 0 {
		return fmt.Errorf("validate container spec: port must be between 1 and 65535: %w", fs.ErrInvalid)
	}
	if s.ChromiumSeccompProfile != "" && !json.Valid([]byte(s.ChromiumSeccompProfile)) {
		return fmt.Errorf("validate Chromium seccomp profile: %w", fs.ErrInvalid)
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

func (s Spec) validateAccess() error {
	switch s.Access {
	case AccessPublished:
		if s.Network != "" {
			return fmt.Errorf("validate container spec: network must be empty for %q access, got %q: %w", s.Access, s.Network, fs.ErrInvalid)
		}
	case AccessNetwork:
		if err := validateNetworkName(s.Network); err != nil {
			return err
		}
	default:
		return fmt.Errorf("validate container spec: access must be %q or %q, got %q: %w", AccessPublished, AccessNetwork, s.Access, fs.ErrInvalid)
	}
	return nil
}

func validateNetworkName(name string) error {
	if name == "" {
		return fmt.Errorf("validate container spec: network is required for %q access: %w", AccessNetwork, fs.ErrInvalid)
	}
	if len(name) > maxNetworkName {
		return fmt.Errorf("validate container spec: network exceeds %d bytes: %w", maxNetworkName, fs.ErrInvalid)
	}
	if !isLetterOrDigit(name[0]) {
		return fmt.Errorf("validate container spec: network %q must start with a letter or digit: %w", name, fs.ErrInvalid)
	}
	for index := 1; index < len(name); index++ {
		char := name[index]
		if !isLetterOrDigit(char) && char != '.' && char != '_' && char != '-' {
			return fmt.Errorf("validate container spec: network %q contains invalid character %q: %w", name, char, fs.ErrInvalid)
		}
	}
	switch name {
	case network.NetworkDefault, network.NetworkBridge, network.NetworkHost, network.NetworkNone:
		return fmt.Errorf("validate container spec: network %q is reserved for Docker network modes: %w", name, fs.ErrInvalid)
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
		observation.FilesDigest = info.Config.Labels["io.cloudlet.files-digest"]
	}
	if observation.Image == "" {
		observation.Image = info.Image
	}
	if info.HostConfig != nil {
		observation.CPUMillis = info.HostConfig.NanoCPUs / 1_000_000
		observation.MemoryBytes = info.HostConfig.Memory
		if info.HostConfig.PidsLimit != nil {
			observation.PIDs = *info.HostConfig.PidsLimit
		}
		for _, limit := range info.HostConfig.Ulimits {
			if limit.Name == "nofile" {
				observation.OpenFiles = limit.Soft
			}
		}
	}
	for _, mount := range info.Mounts {
		if mount.Type == "volume" && mount.RW {
			if observation.WritableVolumes == nil {
				observation.WritableVolumes = make(map[string]string)
			}
			observation.WritableVolumes[mount.Destination] = mount.Name
		}
	}
	ports, err := publishedPorts(info.NetworkSettings)
	if err != nil {
		return Observation{}, fmt.Errorf("observe container %s: %w", info.ID, err)
	}
	observation.Ports = ports
	endpoints, err := networkEndpoints(info)
	if err != nil {
		return Observation{}, fmt.Errorf("observe container %s: %w", info.ID, err)
	}
	observation.Endpoints = endpoints
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

// networkEndpoints reports the actual endpoints on user-defined bridge
// networks. The default bridge is excluded because it is not a managed
// internal network. The managed port comes from the container label so image
// exposed ports do not produce additional endpoints.
func networkEndpoints(info container.InspectResponse) ([]NetworkEndpoint, error) {
	if info.NetworkSettings == nil || info.Config == nil {
		return nil, nil
	}
	port, found, err := managedPort(info.Config.Labels)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	containerName := strings.TrimPrefix(info.Name, "/")
	alias := managedNetworkAlias(containerName)
	var endpoints []NetworkEndpoint
	for networkName, settings := range info.NetworkSettings.Networks {
		if networkName == network.NetworkBridge {
			continue
		}
		if settings == nil {
			return nil, fmt.Errorf("network %q has no endpoint settings: %w", networkName, fs.ErrInvalid)
		}
		ip, found, err := endpointIP(settings)
		if err != nil {
			return nil, fmt.Errorf("network %q: %w", networkName, err)
		}
		if !found {
			continue
		}
		endpoints = append(endpoints, NetworkEndpoint{
			Network:       networkName,
			Name:          endpointName(settings, alias),
			IP:            ip,
			ContainerPort: port.Num(),
			Protocol:      string(port.Proto()),
		})
	}
	slices.SortFunc(endpoints, func(left, right NetworkEndpoint) int {
		if order := strings.Compare(left.Network, right.Network); order != 0 {
			return order
		}
		if order := cmp.Compare(left.ContainerPort, right.ContainerPort); order != 0 {
			return order
		}
		if order := strings.Compare(left.Protocol, right.Protocol); order != 0 {
			return order
		}
		return left.IP.Compare(right.IP)
	})
	return endpoints, nil
}

// managedPort reads the port recorded by the adapter. A container without the
// label was not created with a managed endpoint and reports none.
func managedPort(labels map[string]string) (network.Port, bool, error) {
	value, found := labels[LabelPort]
	if !found {
		return network.Port{}, false, nil
	}
	number, err := strconv.ParseUint(value, 10, 16)
	if err != nil || number == 0 {
		return network.Port{}, false, fmt.Errorf("container label %s has invalid port %q: %w", LabelPort, value, fs.ErrInvalid)
	}
	port, ok := network.PortFrom(uint16(number), network.TCP)
	if !ok {
		return network.Port{}, false, fmt.Errorf("container label %s has invalid port %q: %w", LabelPort, value, fs.ErrInvalid)
	}
	return port, true, nil
}

// endpointName reports the managed DNS alias only when Docker registered it on
// the network. An empty name keeps callers on the observed IP address instead
// of an unverified DNS name.
func endpointName(settings *network.EndpointSettings, alias string) string {
	if slices.Contains(settings.Aliases, alias) || slices.Contains(settings.DNSNames, alias) {
		return alias
	}
	return ""
}

func endpointIP(settings *network.EndpointSettings) (netip.Addr, bool, error) {
	for _, address := range []netip.Addr{settings.IPAddress, settings.GlobalIPv6Address} {
		if !address.IsValid() {
			continue
		}
		if !address.IsGlobalUnicast() {
			return netip.Addr{}, false, fmt.Errorf("address %s is not a unicast address: %w", address, fs.ErrInvalid)
		}
		return address, true, nil
	}
	return netip.Addr{}, false, nil
}
