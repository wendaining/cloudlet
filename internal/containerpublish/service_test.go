package containerpublish

import (
	"errors"
	"io/fs"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/containers"
)

const testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func validRequest() Request {
	return Request{
		Name: "web", Image: "example.com/app@" + testDigest, Port: 8080,
		Hostname: "app.localhost", CPUMillis: 500, MemoryBytes: 128 << 20,
	}
}

func TestValidateRequestNormalizesDefaults(t *testing.T) {
	request, err := ValidateRequest(validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if request.Image != "example.com/app@"+testDigest || request.imageDigest != testDigest {
		t.Fatalf("image = %q digest = %q", request.Image, request.imageDigest)
	}
	if request.User != DefaultUser || request.HealthCheckPath != DefaultHealthPath || request.HealthCheckTimeoutMS != int64(DefaultHealthTimeout/time.Millisecond) {
		t.Fatalf("defaults = user %q path %q timeout %d", request.User, request.HealthCheckPath, request.HealthCheckTimeoutMS)
	}
	if request.Hostname != "app.localhost" {
		t.Fatalf("hostname = %q", request.Hostname)
	}
}

func TestValidateRequestCanonicalizesHostAndImage(t *testing.T) {
	request := validRequest()
	request.Image = "nginx@" + testDigest
	request.Hostname = "APP.Localhost."
	normalized, err := ValidateRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Image != "docker.io/library/nginx@"+testDigest {
		t.Fatalf("image = %q", normalized.Image)
	}
	if normalized.Hostname != "app.localhost" {
		t.Fatalf("hostname = %q", normalized.Hostname)
	}
}

func TestValidateRequestRejectsInvalidInput(t *testing.T) {
	tests := map[string]func(*Request){
		"empty name":           func(r *Request) { r.Name = "" },
		"uppercase name":       func(r *Request) { r.Name = "Web" },
		"double hyphen name":   func(r *Request) { r.Name = "web--app" },
		"tag only image":       func(r *Request) { r.Image = "nginx:latest" },
		"sha512 image":         func(r *Request) { r.Image = "nginx@sha512:" + strings.Repeat("a", 128) },
		"missing image":        func(r *Request) { r.Image = "" },
		"missing port":         func(r *Request) { r.Port = 0 },
		"hostname with port":   func(r *Request) { r.Hostname = "app.localhost:8080" },
		"empty hostname":       func(r *Request) { r.Hostname = "" },
		"long hostname":        func(r *Request) { r.Hostname = strings.Repeat("a", 254) },
		"zero cpu":             func(r *Request) { r.CPUMillis = 0 },
		"zero memory":          func(r *Request) { r.MemoryBytes = 0 },
		"root user":            func(r *Request) { r.User = "0" },
		"root group":           func(r *Request) { r.User = "1000:0" },
		"invalid environment":  func(r *Request) { r.Env = []string{"MISSING_EQUALS"} },
		"invalid env key":      func(r *Request) { r.Env = []string{"1KEY=value"} },
		"duplicate env":        func(r *Request) { r.Env = []string{"KEY=1", "KEY=2"} },
		"command with NUL":     func(r *Request) { r.Command = []string{"bad\x00arg"} },
		"relative health path": func(r *Request) { r.HealthCheckPath = "healthz" },
		"fragment health path": func(r *Request) { r.HealthCheckPath = "/healthz#fragment" },
		"short timeout":        func(r *Request) { r.HealthCheckTimeoutMS = 999 },
		"long timeout":         func(r *Request) { r.HealthCheckTimeoutMS = 60001 },
		"too many commands":    func(r *Request) { r.Command = make([]string, 65) },
		"too many env entries": func(r *Request) { r.Env = make([]string, 129) },
		"invalid PostgreSQL binding": func(r *Request) {
			name := "foreign/db"
			r.PostgresBinding = &name
		},
		"override managed PostgreSQL file": func(r *Request) {
			name := "business-db"
			r.PostgresBinding = &name
			r.Env = []string{postgresURLFileEnv + "=/arbitrary"}
		},
		"volume overlaps PostgreSQL credential file": func(r *Request) {
			name := "business-db"
			r.PostgresBinding = &name
			r.DataVolume = &DataVolumeRequest{MountPath: "/" + postgresURLPath + "/data", Directory: "state"}
		},
		"long user": func(r *Request) { r.User = strings.Repeat("a", 129) },
		"relative volume mount": func(r *Request) {
			r.DataVolume = &DataVolumeRequest{MountPath: "data", Directory: "state"}
		},
		"root volume mount": func(r *Request) {
			r.DataVolume = &DataVolumeRequest{MountPath: "/", Directory: "state"}
		},
		"nested volume directory": func(r *Request) {
			r.DataVolume = &DataVolumeRequest{MountPath: "/data", Directory: "a/b"}
		},
		"non numeric volume user": func(r *Request) {
			r.User = "pwuser"
			r.DataVolume = &DataVolumeRequest{MountPath: "/data", Directory: "state"}
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			request := validRequest()
			change(&request)
			if _, err := ValidateRequest(request); !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validation returned %v", err)
			}
		})
	}
}

func TestPublishedTargetSelectsManagedPort(t *testing.T) {
	observation := containers.Observation{Ports: []containers.PublishedPort{
		{ContainerPort: 8080, Protocol: "tcp", HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: 49152},
	}}
	target, err := publishedTarget(observation, 8080)
	if err != nil {
		t.Fatal(err)
	}
	if target != "http://127.0.0.1:49152" {
		t.Fatalf("target = %q", target)
	}
	if _, err := publishedTarget(observation, 9090); !errors.Is(err, containers.ErrNotReady) {
		t.Fatalf("missing port returned %v", err)
	}
}

func TestAcceptedStatuses(t *testing.T) {
	statuses := acceptedStatuses()
	if len(statuses) != 200 || statuses[0] != 200 || statuses[len(statuses)-1] != 399 {
		t.Fatalf("statuses = %v", statuses)
	}
}
