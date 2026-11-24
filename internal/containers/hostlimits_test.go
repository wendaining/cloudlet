package containers

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func supportedHostInfo() system.Info {
	return system.Info{
		OSType:          "linux",
		Architecture:    "x86_64",
		OperatingSystem: "Ubuntu 24.04",
		ServerVersion:   "29.9.3",
		KernelVersion:   "6.8.0",
		CgroupVersion:   "2",
		CgroupDriver:    "systemd",
		NCPU:            4,
		MemTotal:        4 << 30,
		MemoryLimit:     true,
		SwapLimit:       true,
		CPUCfsPeriod:    true,
		CPUCfsQuota:     true,
		PidsLimit:       true,
	}
}

func TestClassifyHostLimits(t *testing.T) {
	info := supportedHostInfo()
	host, err := classifyHostLimits(info)
	if err != nil {
		t.Fatal(err)
	}
	if host.OSType != info.OSType || host.Architecture != info.Architecture ||
		host.OperatingSystem != info.OperatingSystem || host.ServerVersion != info.ServerVersion ||
		host.KernelVersion != info.KernelVersion || host.CgroupVersion != info.CgroupVersion ||
		host.CgroupDriver != info.CgroupDriver || host.CPUs != info.NCPU || host.MemoryBytes != info.MemTotal ||
		host.SwapLimit != info.SwapLimit {
		t.Fatalf("host fields = %+v, want values from Docker info", host)
	}
	wantOrder := []LimitKind{LimitCPU, LimitMemory, LimitPIDs, LimitOpenFiles}
	if len(host.Limits) != len(wantOrder) {
		t.Fatalf("limit entries = %d, want %d", len(host.Limits), len(wantOrder))
	}
	for index, kind := range wantOrder {
		limit := host.Limits[index]
		if limit.Kind != kind || !limit.Supported || limit.Detail == "" {
			t.Fatalf("limit %d = %+v, want supported %s with detail", index, limit, kind)
		}
		if !host.Supports(kind) {
			t.Fatalf("Supports(%s) = false, want true", kind)
		}
	}
	if host.Supports(LimitKind("unknown")) {
		t.Fatal("Supports(unknown) = true, want false")
	}
}

func TestClassifyHostLimitsUnsupported(t *testing.T) {
	tests := []struct {
		name       string
		change     func(*system.Info)
		kind       LimitKind
		detailPart string
	}{
		{
			name:       "CPU quota",
			change:     func(info *system.Info) { info.CPUCfsQuota = false },
			kind:       LimitCPU,
			detailPart: "no CPU CFS quota support",
		},
		{
			name:       "CPU period",
			change:     func(info *system.Info) { info.CPUCfsPeriod = false },
			kind:       LimitCPU,
			detailPart: "no CPU CFS period support",
		},
		{
			name:       "CPU quota and period",
			change:     func(info *system.Info) { info.CPUCfsQuota = false; info.CPUCfsPeriod = false },
			kind:       LimitCPU,
			detailPart: "no CPU CFS quota or period support",
		},
		{
			name:       "memory",
			change:     func(info *system.Info) { info.MemoryLimit = false },
			kind:       LimitMemory,
			detailPart: "no memory limit support",
		},
		{
			name:       "pids",
			change:     func(info *system.Info) { info.PidsLimit = false },
			kind:       LimitPIDs,
			detailPart: "no pids limit support",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := supportedHostInfo()
			test.change(&info)
			host, err := classifyHostLimits(info)
			if err != nil {
				t.Fatal(err)
			}
			if host.Supports(test.kind) {
				t.Fatalf("Supports(%s) = true, want false", test.kind)
			}
			var detail string
			for _, limit := range host.Limits {
				if limit.Kind == test.kind {
					detail = limit.Detail
				}
			}
			if !strings.Contains(detail, test.detailPart) {
				t.Fatalf("detail = %q, want it to contain %q", detail, test.detailPart)
			}
			for _, kind := range []LimitKind{LimitCPU, LimitMemory, LimitPIDs, LimitOpenFiles} {
				if kind == test.kind {
					continue
				}
				if !host.Supports(kind) {
					t.Fatalf("Supports(%s) = false after changing %s, want true", kind, test.kind)
				}
			}
		})
	}
}

func TestClassifyHostLimitsReportsSwapSupport(t *testing.T) {
	info := supportedHostInfo()
	info.SwapLimit = false
	host, err := classifyHostLimits(info)
	if err != nil {
		t.Fatal(err)
	}
	if host.SwapLimit {
		t.Fatal("SwapLimit = true, want false")
	}
	if !host.Supports(LimitMemory) {
		t.Fatal("memory limit support must not depend on swap limit support")
	}
	var detail string
	for _, limit := range host.Limits {
		if limit.Kind == LimitMemory {
			detail = limit.Detail
		}
	}
	if !strings.Contains(detail, "swap limit: false") {
		t.Fatalf("memory detail = %q, want it to report swap limit support", detail)
	}
}

func TestClassifyHostLimitsRejectsNonLinux(t *testing.T) {
	for _, osType := range []string{"windows", ""} {
		info := supportedHostInfo()
		info.OSType = osType
		if _, err := classifyHostLimits(info); err == nil || !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("classifyHostLimits(%q) = %v, want fs.ErrInvalid", osType, err)
		}
	}
}

func TestProbeHostLimitsRejectsNilClient(t *testing.T) {
	if _, err := ProbeHostLimits(context.Background(), nil); err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("ProbeHostLimits(nil) = %v, want fs.ErrInvalid", err)
	}
}

func TestProbeHostLimitsReportsDockerInfoError(t *testing.T) {
	docker, err := client.New(client.WithHost("tcp://127.0.0.1:1"), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := ProbeHostLimits(ctx, docker); err == nil || !strings.Contains(err.Error(), "read Docker info") {
		t.Fatalf("ProbeHostLimits with unreachable Docker = %v, want a Docker info error", err)
	}
}

func TestHostLimitsZeroValueHasNoSupport(t *testing.T) {
	var host HostLimits
	for _, kind := range []LimitKind{LimitCPU, LimitMemory, LimitPIDs, LimitOpenFiles} {
		if host.Supports(kind) {
			t.Fatalf("zero HostLimits supports %s, want false", kind)
		}
	}
}
