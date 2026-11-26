package containers

import (
	"errors"
	"io/fs"
	"maps"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/network"
)

func validNetworkSpec() NetworkSpec {
	return NetworkSpec{Environment: "default", Project: "project-1"}
}

func TestNetworkSpecValidate(t *testing.T) {
	if err := validNetworkSpec().validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*NetworkSpec)
	}{
		{"missing environment", func(spec *NetworkSpec) { spec.Environment = "" }},
		{"missing project", func(spec *NetworkSpec) { spec.Project = "" }},
		{"environment with slash", func(spec *NetworkSpec) { spec.Environment = "dev/one" }},
		{"project starting with dash", func(spec *NetworkSpec) { spec.Project = "-project" }},
		{"project with space", func(spec *NetworkSpec) { spec.Project = "project 1" }},
		{"environment too long", func(spec *NetworkSpec) { spec.Environment = strings.Repeat("a", maxIdentifierLength+1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := validNetworkSpec()
			test.change(&spec)
			if err := spec.validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validate() = %v, want fs.ErrInvalid", err)
			}
			if err := spec.Validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("Validate() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestNetworkSpecName(t *testing.T) {
	spec := validNetworkSpec()
	if got, want := spec.Name(), "cloudlet-net-7-default-9-project-1"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got := spec.Name(); got != spec.Name() {
		t.Fatalf("Name() is not deterministic: %q then %q", got, spec.Name())
	}
	// Length prefixes keep identifiers containing hyphens unambiguous.
	left := NetworkSpec{Environment: "a", Project: "bc"}
	right := NetworkSpec{Environment: "ab", Project: "c"}
	if left.Name() == right.Name() {
		t.Fatalf("network names collide: %q", left.Name())
	}
	longest := NetworkSpec{
		Environment: strings.Repeat("a", maxIdentifierLength),
		Project:     strings.Repeat("b", maxIdentifierLength),
	}
	if err := longest.validate(); err != nil {
		t.Fatal(err)
	}
	if len(longest.Name()) > maxNetworkName {
		t.Fatalf("longest network name is %d bytes, limit is %d", len(longest.Name()), maxNetworkName)
	}
}

func TestNetworkSpecLabels(t *testing.T) {
	spec := validNetworkSpec()
	labels := spec.labels()
	want := map[string]string{
		LabelManaged:     "true",
		LabelEnvironment: "default",
		LabelComponent:   ComponentRuntime,
		LabelProject:     "project-1",
		LabelResource:    NetworkResource,
	}
	if !maps.Equal(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	if got, want := len(spec.labelPairs()), len(want); got != want {
		t.Fatalf("labelPairs() has %d entries, want %d", got, want)
	}
}

func TestVerifyProjectNetwork(t *testing.T) {
	spec := validNetworkSpec()
	valid := network.Network{
		Name:   spec.Name(),
		Scope:  "local",
		Driver: "bridge",
		Labels: spec.labels(),
	}
	if err := verifyProjectNetwork(spec.Name(), valid, spec); err != nil {
		t.Fatal(err)
	}
	withExtra := valid
	withExtra.Labels = maps.Clone(valid.Labels)
	withExtra.Labels["org.example.extra"] = "value"
	if err := verifyProjectNetwork(spec.Name(), withExtra, spec); err != nil {
		t.Fatalf("verifyProjectNetwork() with extra label = %v", err)
	}

	tests := []struct {
		name   string
		change func(*network.Network)
		want   error
	}{
		{"missing managed label", func(info *network.Network) { delete(info.Labels, LabelManaged) }, ErrNetworkNotOwned},
		{"wrong environment", func(info *network.Network) { info.Labels[LabelEnvironment] = "other" }, ErrNetworkNotOwned},
		{"wrong project", func(info *network.Network) { info.Labels[LabelProject] = "other" }, ErrNetworkNotOwned},
		{"wrong component", func(info *network.Network) { info.Labels[LabelComponent] = "database" }, ErrNetworkNotOwned},
		{"wrong resource", func(info *network.Network) { info.Labels[LabelResource] = "container" }, ErrNetworkNotOwned},
		{"nil labels", func(info *network.Network) { info.Labels = nil }, ErrNetworkNotOwned},
		{"name mismatch", func(info *network.Network) { info.Name = "other" }, ErrNetworkNotFound},
		{"swarm scope", func(info *network.Network) { info.Scope = "swarm" }, fs.ErrInvalid},
		{"config only", func(info *network.Network) { info.ConfigOnly = true }, fs.ErrInvalid},
		{"non-bridge driver", func(info *network.Network) { info.Driver = "macvlan" }, fs.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := valid
			info.Labels = maps.Clone(valid.Labels)
			test.change(&info)
			err := verifyProjectNetwork(spec.Name(), info, spec)
			if !errors.Is(err, test.want) {
				t.Fatalf("verifyProjectNetwork() = %v, want %v", err, test.want)
			}
		})
	}

	// A managed network of the same project under a non-canonical name must not
	// be accepted as the project network.
	nonCanonical := valid
	nonCanonical.Name = "cloudlet-net-other"
	if err := verifyProjectNetwork(nonCanonical.Name, nonCanonical, spec); !errors.Is(err, ErrNetworkNotOwned) {
		t.Fatalf("verifyProjectNetwork() with non-canonical name = %v, want ErrNetworkNotOwned", err)
	}
}

func TestNetworkFromInspect(t *testing.T) {
	info := network.Inspect{Network: network.Network{
		ID:   strings.Repeat("a", 64),
		Name: validNetworkSpec().Name(),
	}}
	got, err := networkFromInspect(info)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != info.ID || got.Name != info.Name {
		t.Fatalf("networkFromInspect() = %+v", got)
	}
	for name, change := range map[string]func(*network.Inspect){
		"short ID": func(info *network.Inspect) { info.ID = "short" },
		"invalid ID": func(info *network.Inspect) {
			info.ID = strings.Repeat("z", 64)
		},
		"missing name": func(info *network.Inspect) { info.Name = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := info
			change(&changed)
			if _, err := networkFromInspect(changed); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("networkFromInspect() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestValidateNetworkID(t *testing.T) {
	for _, id := range []string{"0123456789ab", strings.Repeat("a", 64), "0123456789AB"} {
		if err := validateNetworkID(id); err != nil {
			t.Fatalf("validateNetworkID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", "short", strings.Repeat("a", 65), "0123456789az"} {
		if err := validateNetworkID(id); err == nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("validateNetworkID(%q) = %v, want fs.ErrInvalid", id, err)
		}
	}
}
