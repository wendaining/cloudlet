package containers

import (
	"errors"
	"io/fs"
	"maps"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
)

func validOwnership() Ownership {
	return Ownership{
		Environment: "default",
		Project:     "project-1",
		Resource:    "resource-1",
		Generation:  1,
	}
}

func validSpec() Spec {
	return Spec{
		Ownership: validOwnership(),
		Image:     "nginx:1.29.3-alpine",
		User:      "101",
		Command:   []string{"run"},
		Env:       []string{"A=1"},
		Port:      8080,
		Access:    AccessPublished,
		Limits: Limits{
			CPUMillis:   500,
			MemoryBytes: 64 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
}

func TestOwnershipValidate(t *testing.T) {
	if err := validOwnership().validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*Ownership)
	}{
		{"missing environment", func(o *Ownership) { o.Environment = "" }},
		{"missing project", func(o *Ownership) { o.Project = "" }},
		{"missing resource", func(o *Ownership) { o.Resource = "" }},
		{"zero generation", func(o *Ownership) { o.Generation = 0 }},
		{"environment with slash", func(o *Ownership) { o.Environment = "dev/one" }},
		{"project starting with dash", func(o *Ownership) { o.Project = "-project" }},
		{"resource with space", func(o *Ownership) { o.Resource = "resource 1" }},
		{"environment too long", func(o *Ownership) { o.Environment = strings.Repeat("a", maxIdentifierLength+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ownership := validOwnership()
			test.change(&ownership)
			if err := ownership.validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validate() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestOwnershipLabelsAndVerify(t *testing.T) {
	ownership := validOwnership()
	labels := ownership.labels()
	want := map[string]string{
		LabelManaged:     "true",
		LabelEnvironment: "default",
		LabelComponent:   ComponentRuntime,
		LabelProject:     "project-1",
		LabelResource:    "resource-1",
		LabelGeneration:  "1",
	}
	if !maps.Equal(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	if err := ownership.verifyLabels(labels); err != nil {
		t.Fatal(err)
	}
	withExtra := maps.Clone(labels)
	withExtra["org.example.extra"] = "value"
	if err := ownership.verifyLabels(withExtra); err != nil {
		t.Fatalf("verifyLabels() with extra label = %v", err)
	}
	for name, change := range map[string]func(map[string]string){
		"missing label": func(labels map[string]string) { delete(labels, LabelProject) },
		"wrong label":   func(labels map[string]string) { labels[LabelGeneration] = "2" },
		"nil labels":    func(map[string]string) {},
	} {
		t.Run(name, func(t *testing.T) {
			labels := maps.Clone(withExtra)
			change(labels)
			if name == "nil labels" {
				labels = nil
			}
			if err := ownership.verifyLabels(labels); !errors.Is(err, ErrNotOwned) {
				t.Fatalf("verifyLabels() = %v, want ErrNotOwned", err)
			}
		})
	}
}

func TestOwnershipVerify(t *testing.T) {
	ownership := validOwnership()
	info := container.InspectResponse{
		Name: "/" + ownership.containerName(),
		Config: &container.Config{
			Labels: ownership.labels(),
		},
	}
	if err := ownership.verify(info); err != nil {
		t.Fatal(err)
	}

	info.Name = "/foreign-container"
	if err := ownership.verify(info); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("verify() with mismatched name = %v, want ErrNotOwned", err)
	}

	info.Name = "/" + ownership.containerName()
	info.Config = nil
	if err := ownership.verify(info); err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("verify() without config = %v, want fs.ErrInvalid", err)
	}
}

func TestSpecValidate(t *testing.T) {
	if err := validSpec().validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*Spec)
	}{
		{"invalid ownership", func(s *Spec) { s.Ownership.Generation = 0 }},
		{"missing image", func(s *Spec) { s.Image = "" }},
		{"image with whitespace", func(s *Spec) { s.Image = "nginx alpine" }},
		{"image too long", func(s *Spec) { s.Image = strings.Repeat("a", maxImageLength+1) }},
		{"missing user", func(s *Spec) { s.User = "" }},
		{"root user", func(s *Spec) { s.User = "root" }},
		{"numeric root user", func(s *Spec) { s.User = "0" }},
		{"numeric root user with group", func(s *Spec) { s.User = "0:1000" }},
		{"root group", func(s *Spec) { s.User = "1000:0" }},
		{"user with whitespace", func(s *Spec) { s.User = "app user" }},
		{"missing access", func(s *Spec) { s.Access = "" }},
		{"unknown access", func(s *Spec) { s.Access = "host" }},
		{"network access without network", func(s *Spec) { s.Access = AccessNetwork }},
		{"network access with empty network", func(s *Spec) { s.Access = AccessNetwork; s.Network = "" }},
		{"network access with reserved bridge", func(s *Spec) { s.Access = AccessNetwork; s.Network = "bridge" }},
		{"network access with reserved host", func(s *Spec) { s.Access = AccessNetwork; s.Network = "host" }},
		{"network access with reserved none", func(s *Spec) { s.Access = AccessNetwork; s.Network = "none" }},
		{"network access with reserved default", func(s *Spec) { s.Access = AccessNetwork; s.Network = "default" }},
		{"network access with leading dash", func(s *Spec) { s.Access = AccessNetwork; s.Network = "-network" }},
		{"network access with slash", func(s *Spec) { s.Access = AccessNetwork; s.Network = "project/network" }},
		{"network access with space", func(s *Spec) { s.Access = AccessNetwork; s.Network = "project network" }},
		{"network access with too long name", func(s *Spec) {
			s.Access = AccessNetwork
			s.Network = strings.Repeat("a", maxNetworkName+1)
		}},
		{"published access with network", func(s *Spec) { s.Network = "cloudlet-network" }},
		{"zero port", func(s *Spec) { s.Port = 0 }},
		{"zero cpu", func(s *Spec) { s.Limits.CPUMillis = 0 }},
		{"cpu above maximum", func(s *Spec) { s.Limits.CPUMillis = 1_000_001 }},
		{"zero memory", func(s *Spec) { s.Limits.MemoryBytes = 0 }},
		{"zero pids", func(s *Spec) { s.Limits.PIDs = 0 }},
		{"zero open files", func(s *Spec) { s.Limits.OpenFiles = 0 }},
		{"command with nul", func(s *Spec) { s.Command = []string{"run\x00"} }},
		{"environment without equals", func(s *Spec) { s.Env = []string{"A"} }},
		{"environment with empty key", func(s *Spec) { s.Env = []string{"=1"} }},
		{"environment with invalid key", func(s *Spec) { s.Env = []string{"1A=1"} }},
		{"environment with dash", func(s *Spec) { s.Env = []string{"A-B=1"} }},
		{"duplicate environment", func(s *Spec) { s.Env = []string{"A=1", "A=2"} }},
		{"environment with nul", func(s *Spec) { s.Env = []string{"A=\x00"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validSpec()
			test.change(&spec)
			if err := spec.validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validate() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestContainerNameDistinguishesOwnershipFields(t *testing.T) {
	left := Ownership{Environment: "default", Project: "alpha-beta", Resource: "gamma", Generation: 1}
	right := Ownership{Environment: "default", Project: "alpha", Resource: "beta-gamma", Generation: 1}
	if left.containerName() == right.containerName() {
		t.Fatalf("不同归属得到相同容器名称: %q", left.containerName())
	}
	if got, want := left.containerName(), "cloudlet-7-default-10-alpha-beta-5-gamma-1"; got != want {
		t.Fatalf("containerName() = %q, want %q", got, want)
	}
	if got, want := right.containerName(), "cloudlet-7-default-5-alpha-10-beta-gamma-1"; got != want {
		t.Fatalf("containerName() = %q, want %q", got, want)
	}
}

func TestCreateOptions(t *testing.T) {
	spec := validSpec()
	options, err := spec.createOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.Name != "cloudlet-7-default-9-project-1-10-resource-1-1" {
		t.Fatalf("name = %q", options.Name)
	}
	if options.Config == nil || options.HostConfig == nil {
		t.Fatal("create options must include config and host config")
	}
	if options.Config.Image != spec.Image || options.Config.User != spec.User {
		t.Fatalf("config image/user = %q/%q", options.Config.Image, options.Config.User)
	}
	for key, want := range spec.Ownership.labels() {
		if got := options.Config.Labels[key]; got != want {
			t.Fatalf("label %s = %q, want %q", key, got, want)
		}
	}
	if options.Config.Labels[LabelPort] != "8080" {
		t.Fatalf("port label = %q, want 8080", options.Config.Labels[LabelPort])
	}
	port := network.MustParsePort("8080/tcp")
	if _, found := options.Config.ExposedPorts[port]; !found {
		t.Fatalf("exposed ports = %v, want %s", options.Config.ExposedPorts, port)
	}
	bindings := options.HostConfig.PortBindings[port]
	if len(bindings) != 1 {
		t.Fatalf("port bindings = %v", bindings)
	}
	if bindings[0].HostIP != netip.MustParseAddr(loopbackHost) || bindings[0].HostPort != "" {
		t.Fatalf("binding = %+v, want loopback with an ephemeral port", bindings[0])
	}
	if options.HostConfig.NetworkMode != network.NetworkBridge {
		t.Fatalf("network mode = %q, want bridge", options.HostConfig.NetworkMode)
	}
	if options.NetworkingConfig != nil {
		t.Fatalf("published access must not set a networking config: %+v", options.NetworkingConfig)
	}
	if options.HostConfig.AutoRemove {
		t.Fatal("managed containers must not auto-remove")
	}
	if !options.HostConfig.RestartPolicy.IsNone() {
		t.Fatalf("restart policy = %+v, want disabled", options.HostConfig.RestartPolicy)
	}
	if options.Config.StopTimeout == nil || *options.Config.StopTimeout != int(stopTimeout.Seconds()) {
		t.Fatalf("stop timeout = %v", options.Config.StopTimeout)
	}
	resources := options.HostConfig.Resources
	if resources.NanoCPUs != spec.Limits.CPUMillis*1_000_000 {
		t.Fatalf("nano CPUs = %d", resources.NanoCPUs)
	}
	if resources.Memory != spec.Limits.MemoryBytes {
		t.Fatalf("memory = %d", resources.Memory)
	}
	if resources.PidsLimit == nil || *resources.PidsLimit != spec.Limits.PIDs {
		t.Fatalf("pids limit = %v", resources.PidsLimit)
	}
	if len(resources.Ulimits) != 1 || resources.Ulimits[0].Name != "nofile" ||
		resources.Ulimits[0].Soft != spec.Limits.OpenFiles || resources.Ulimits[0].Hard != spec.Limits.OpenFiles {
		t.Fatalf("ulimits = %+v", resources.Ulimits)
	}
	spec.Command[0] = "changed"
	spec.Env[0] = "CHANGED=1"
	if options.Config.Cmd[0] != "run" || options.Config.Env[0] != "A=1" {
		t.Fatalf("create options share command or environment slices: %v %v", options.Config.Cmd, options.Config.Env)
	}
}

func TestCreateOptionsNetwork(t *testing.T) {
	spec := validSpec()
	spec.Access = AccessNetwork
	spec.Network = "cloudlet-execution"
	options, err := spec.createOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.Name != "cloudlet-7-default-9-project-1-10-resource-1-1" {
		t.Fatalf("name = %q", options.Name)
	}
	if options.HostConfig == nil || options.Config == nil {
		t.Fatal("create options must include config and host config")
	}
	if options.HostConfig.NetworkMode != container.NetworkMode("cloudlet-execution") {
		t.Fatalf("network mode = %q, want cloudlet-execution", options.HostConfig.NetworkMode)
	}
	if len(options.HostConfig.PortBindings) != 0 {
		t.Fatalf("network access must not publish ports: %+v", options.HostConfig.PortBindings)
	}
	if options.NetworkingConfig == nil {
		t.Fatal("network access must set a networking config")
	}
	endpoint, found := options.NetworkingConfig.EndpointsConfig["cloudlet-execution"]
	if !found {
		t.Fatalf("endpoint config = %+v, want cloudlet-execution", options.NetworkingConfig.EndpointsConfig)
	}
	wantAlias := spec.Ownership.networkAlias()
	if !slices.Contains(endpoint.Aliases, wantAlias) {
		t.Fatalf("aliases = %v, want %q", endpoint.Aliases, wantAlias)
	}
	if !validDNSLabel(wantAlias) {
		t.Fatalf("network alias %q is not a valid DNS label", wantAlias)
	}
	port := network.MustParsePort("8080/tcp")
	if _, found := options.Config.ExposedPorts[port]; !found {
		t.Fatalf("exposed ports = %v, want %s", options.Config.ExposedPorts, port)
	}
	if options.HostConfig.AutoRemove {
		t.Fatal("managed containers must not auto-remove")
	}
	if !options.HostConfig.RestartPolicy.IsNone() {
		t.Fatalf("restart policy = %+v, want disabled", options.HostConfig.RestartPolicy)
	}
}

func TestNetworkAliasIsStableAndUnique(t *testing.T) {
	ownership := validOwnership()
	alias := ownership.networkAlias()
	if alias != ownership.networkAlias() {
		t.Fatalf("network alias is not stable: %q != %q", alias, ownership.networkAlias())
	}
	if !validDNSLabel(alias) {
		t.Fatalf("network alias %q is not a valid DNS label", alias)
	}
	other := ownership
	other.Generation = 2
	if other.networkAlias() == alias {
		t.Fatalf("network alias %q does not distinguish generations", alias)
	}
	long := ownership
	long.Resource = strings.Repeat("a", maxIdentifierLength)
	if !validDNSLabel(long.networkAlias()) {
		t.Fatalf("network alias %q is not a valid DNS label", long.networkAlias())
	}
}

func TestObserve(t *testing.T) {
	info := container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/cloudlet-7-default-9-project-1-10-resource-1-1",
		Config: &container.Config{
			Image:  "nginx:1.29.3-alpine",
			Labels: validOwnership().labels(),
		},
		State: &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Ports: network.PortMap{
				network.MustParsePort("8443/tcp"): {{HostIP: netip.MustParseAddr("::1"), HostPort: "49153"}},
				network.MustParsePort("8080/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "49152"}},
			},
		},
	}
	observation, err := observe(info)
	if err != nil {
		t.Fatal(err)
	}
	if observation.ID != info.ID || observation.Name != strings.TrimPrefix(info.Name, "/") {
		t.Fatalf("observation identity = %+v", observation)
	}
	if observation.Image != info.Config.Image || !observation.Running || observation.Status != "running" {
		t.Fatalf("observation state = %+v", observation)
	}
	if len(observation.Ports) != 2 {
		t.Fatalf("ports = %+v", observation.Ports)
	}
	if observation.Ports[0].ContainerPort != 8080 || observation.Ports[0].HostAddress() != "127.0.0.1:49152" {
		t.Fatalf("first port = %+v", observation.Ports[0])
	}
	if observation.Ports[1].ContainerPort != 8443 || observation.Ports[1].HostAddress() != "[::1]:49153" {
		t.Fatalf("second port = %+v", observation.Ports[1])
	}
	if observation.Endpoints != nil {
		t.Fatalf("endpoints = %+v, want none for the default bridge", observation.Endpoints)
	}
}

func TestObserveNetworkEndpoints(t *testing.T) {
	const containerName = "cloudlet-7-default-9-project-1-10-resource-1-1"
	alias := managedNetworkAlias(containerName)
	info := container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/" + containerName,
		Config: &container.Config{
			Image:        "nginx:1.29.3-alpine",
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}, network.MustParsePort("9090/tcp"): {}},
			Labels:       map[string]string{LabelPort: "8080"},
		},
		State: &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: netip.MustParseAddr("172.17.0.2")},
				"cloudlet-execution": {
					IPAddress: netip.MustParseAddr("172.18.0.2"),
					Aliases:   []string{alias},
				},
				"cloudlet-other": {
					GlobalIPv6Address: netip.MustParseAddr("fd00::2"),
				},
			},
		},
	}
	observation, err := observe(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want 2", observation.Endpoints)
	}
	want := []NetworkEndpoint{
		{Network: "cloudlet-execution", Name: alias, IP: netip.MustParseAddr("172.18.0.2"), ContainerPort: 8080, Protocol: "tcp"},
		{Network: "cloudlet-other", Name: "", IP: netip.MustParseAddr("fd00::2"), ContainerPort: 8080, Protocol: "tcp"},
	}
	if !slices.Equal(observation.Endpoints, want) {
		t.Fatalf("endpoints = %+v, want %+v", observation.Endpoints, want)
	}
	if got, want := observation.Endpoints[0].Address(), alias+":8080"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}
	if got, want := observation.Endpoints[0].IPAddress(), "172.18.0.2:8080"; got != want {
		t.Fatalf("IPAddress() = %q, want %q", got, want)
	}
	if got, want := observation.Endpoints[1].Address(), "[fd00::2]:8080"; got != want {
		t.Fatalf("Address() without alias = %q, want %q", got, want)
	}
	if got, want := observation.Endpoints[1].IPAddress(), "[fd00::2]:8080"; got != want {
		t.Fatalf("IPv6 IPAddress() = %q, want %q", got, want)
	}
}

func TestObserveNetworkEndpointFromDNSNames(t *testing.T) {
	const containerName = "cloudlet-7-default-9-project-1-10-resource-1-1"
	alias := managedNetworkAlias(containerName)
	observation, err := observe(container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/" + containerName,
		Config: &container.Config{
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}},
			Labels:       map[string]string{LabelPort: "8080"},
		},
		State: &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"cloudlet-execution": {
					IPAddress: netip.MustParseAddr("172.18.0.2"),
					DNSNames:  []string{alias},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Endpoints) != 1 || observation.Endpoints[0].Name != alias {
		t.Fatalf("endpoints = %+v, want alias %q", observation.Endpoints, alias)
	}
}

func TestObserveNetworkEndpointWithoutAddress(t *testing.T) {
	observation, err := observe(container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/cloudlet-7-default-9-project-1-10-resource-1-1",
		Config: &container.Config{
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}},
			Labels:       map[string]string{LabelPort: "8080"},
		},
		State: &container.State{Status: container.StateExited},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"cloudlet-execution": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Endpoints != nil {
		t.Fatalf("endpoints = %+v, want none for a stopped container", observation.Endpoints)
	}
}

func TestObserveRejectsInvalidNetworkData(t *testing.T) {
	base := container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/cloudlet-7-default-9-project-1-10-resource-1-1",
		Config: &container.Config{
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}},
			Labels:       map[string]string{LabelPort: "8080"},
		},
		State: &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"cloudlet-execution": {IPAddress: netip.MustParseAddr("172.18.0.2")},
			},
		},
	}
	tests := []struct {
		name   string
		change func(*container.InspectResponse)
	}{
		{"nil endpoint settings", func(info *container.InspectResponse) {
			info.NetworkSettings.Networks["cloudlet-execution"] = nil
		}},
		{"loopback address", func(info *container.InspectResponse) {
			info.NetworkSettings.Networks["cloudlet-execution"].IPAddress = netip.MustParseAddr("127.0.0.1")
		}},
		{"unspecified address", func(info *container.InspectResponse) {
			info.NetworkSettings.Networks["cloudlet-execution"].IPAddress = netip.MustParseAddr("0.0.0.0")
		}},
		{"link-local address", func(info *container.InspectResponse) {
			info.NetworkSettings.Networks["cloudlet-execution"].IPAddress = netip.MustParseAddr("169.254.1.1")
		}},
		{"invalid port label", func(info *container.InspectResponse) {
			info.Config.Labels[LabelPort] = "http"
		}},
		{"zero port label", func(info *container.InspectResponse) {
			info.Config.Labels[LabelPort] = "0"
		}},
		{"port label with protocol", func(info *container.InspectResponse) {
			info.Config.Labels[LabelPort] = "8080/tcp"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := base
			info.Config = &container.Config{
				ExposedPorts: base.Config.ExposedPorts,
				Labels:       maps.Clone(base.Config.Labels),
			}
			networks := map[string]*network.EndpointSettings{}
			for name, settings := range base.NetworkSettings.Networks {
				clone := *settings
				networks[name] = &clone
			}
			info.NetworkSettings = &container.NetworkSettings{Networks: networks}
			test.change(&info)
			if _, err := observe(info); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("observe() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestObserveNetworkWithoutManagedPort(t *testing.T) {
	observation, err := observe(container.InspectResponse{
		ID:     "0123456789abcdef",
		Name:   "/cloudlet-7-default-9-project-1-10-resource-1-1",
		Config: &container.Config{ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}}},
		State:  &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"cloudlet-execution": {IPAddress: netip.MustParseAddr("172.18.0.2")},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Endpoints != nil {
		t.Fatalf("endpoints = %+v, want none without a managed port label", observation.Endpoints)
	}
}

func TestObserveRejectsInvalidInspectData(t *testing.T) {
	base := container.InspectResponse{
		ID:     "0123456789abcdef",
		Config: &container.Config{Image: "image"},
		State:  &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Ports: network.PortMap{
				network.MustParsePort("8080/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "49152"}},
			},
		},
	}
	tests := []struct {
		name   string
		change func(*container.InspectResponse)
	}{
		{"missing state", func(info *container.InspectResponse) { info.State = nil }},
		{"missing host address", func(info *container.InspectResponse) {
			info.NetworkSettings.Ports[network.MustParsePort("8080/tcp")] = []network.PortBinding{{HostPort: "49152"}}
		}},
		{"non-loopback address", func(info *container.InspectResponse) {
			info.NetworkSettings.Ports[network.MustParsePort("8080/tcp")] = []network.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: "49152"}}
		}},
		{"invalid host port", func(info *container.InspectResponse) {
			info.NetworkSettings.Ports[network.MustParsePort("8080/tcp")] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "http"}}
		}},
		{"zero host port", func(info *container.InspectResponse) {
			info.NetworkSettings.Ports[network.MustParsePort("8080/tcp")] = []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "0"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := base
			ports := network.PortMap{}
			for port, bindings := range base.NetworkSettings.Ports {
				ports[port] = slices.Clone(bindings)
			}
			info.NetworkSettings = &container.NetworkSettings{Ports: ports}
			test.change(&info)
			if _, err := observe(info); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("observe() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestObserveWithoutNetworkSettings(t *testing.T) {
	observation, err := observe(container.InspectResponse{
		ID:     "0123456789abcdef",
		Config: &container.Config{Image: "image"},
		State:  &container.State{Status: container.StateCreated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Ports != nil {
		t.Fatalf("ports = %+v, want none", observation.Ports)
	}
	if observation.Endpoints != nil {
		t.Fatalf("endpoints = %+v, want none", observation.Endpoints)
	}
}

func TestValidateContainerID(t *testing.T) {
	for _, id := range []string{"0123456789ab", strings.Repeat("a", 64), "0123456789AB"} {
		if err := validateContainerID(id); err != nil {
			t.Fatalf("validateContainerID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "short", strings.Repeat("a", 65), "0123456789az"} {
		if err := validateContainerID(id); err == nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("validateContainerID(%q) = %v, want fs.ErrInvalid", id, err)
		}
	}
}

func TestSpecValidateAcceptsNetworkAccess(t *testing.T) {
	for _, name := range []string{"a", "cloudlet-execution", "cloudlet_net.1", strings.Repeat("a", maxNetworkName)} {
		spec := validSpec()
		spec.Access = AccessNetwork
		spec.Network = name
		if err := spec.validate(); err != nil {
			t.Fatalf("validate network %q: %v", name, err)
		}
	}
}

func TestVerifyNetwork(t *testing.T) {
	valid := network.Inspect{Network: network.Network{Name: "cloudlet-execution", Scope: "local", Driver: "bridge"}}
	if err := verifyNetwork("cloudlet-execution", valid); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*network.Inspect)
	}{
		{"name mismatch", func(info *network.Inspect) { info.Name = "other" }},
		{"swarm scope", func(info *network.Inspect) { info.Scope = "swarm" }},
		{"config only", func(info *network.Inspect) { info.ConfigOnly = true }},
		{"non-bridge driver", func(info *network.Inspect) { info.Driver = "macvlan" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := valid
			test.change(&info)
			if err := verifyNetwork("cloudlet-execution", info); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("verifyNetwork() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestObserveNetworkEndpointWithoutAlias(t *testing.T) {
	const containerName = "cloudlet-7-default-9-project-1-10-resource-1-1"
	observation, err := observe(container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/" + containerName,
		Config: &container.Config{
			ExposedPorts: network.PortSet{network.MustParsePort("8080/tcp"): {}},
			Labels:       map[string]string{LabelPort: "8080"},
		},
		State: &container.State{Status: container.StateRunning, Running: true},
		NetworkSettings: &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"cloudlet-execution": {IPAddress: netip.MustParseAddr("172.18.0.2")},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observation.Endpoints) != 1 {
		t.Fatalf("endpoints = %+v, want 1", observation.Endpoints)
	}
	endpoint := observation.Endpoints[0]
	if endpoint.Name != "" {
		t.Fatalf("endpoint name = %q, want empty without a registered alias", endpoint.Name)
	}
	if got, want := endpoint.Address(), "172.18.0.2:8080"; got != want {
		t.Fatalf("Address() = %q, want %q", got, want)
	}
}

func TestManagedNetworkAliasFormat(t *testing.T) {
	alias := validOwnership().networkAlias()
	if !strings.HasPrefix(alias, "cloudlet-") {
		t.Fatalf("network alias %q does not use the cloudlet prefix", alias)
	}
	if len(alias) != len("cloudlet-")+32 {
		t.Fatalf("network alias %q has length %d, want %d", alias, len(alias), len("cloudlet-")+32)
	}
	if !validDNSLabel(alias) {
		t.Fatalf("network alias %q is not a valid DNS label", alias)
	}
}

func validDNSLabel(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-') {
			return false
		}
	}
	return true
}
