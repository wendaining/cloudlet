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
	if err := ownership.verify(labels); err != nil {
		t.Fatal(err)
	}
	withExtra := maps.Clone(labels)
	withExtra["org.example.extra"] = "value"
	if err := ownership.verify(withExtra); err != nil {
		t.Fatalf("verify() with extra label = %v", err)
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
			if err := ownership.verify(labels); !errors.Is(err, ErrNotOwned) {
				t.Fatalf("verify() = %v, want ErrNotOwned", err)
			}
		})
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

func TestCreateOptions(t *testing.T) {
	spec := validSpec()
	options, err := spec.createOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.Name != "cloudlet-default-project-1-resource-1-1" {
		t.Fatalf("name = %q", options.Name)
	}
	if options.Config == nil || options.HostConfig == nil {
		t.Fatal("create options must include config and host config")
	}
	if options.Config.Image != spec.Image || options.Config.User != spec.User {
		t.Fatalf("config image/user = %q/%q", options.Config.Image, options.Config.User)
	}
	if !maps.Equal(options.Config.Labels, spec.Ownership.labels()) {
		t.Fatalf("labels = %v", options.Config.Labels)
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

func TestObserve(t *testing.T) {
	info := container.InspectResponse{
		ID:   "0123456789abcdef",
		Name: "/cloudlet-default-project-1-resource-1-1",
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
