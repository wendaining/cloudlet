package containers

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// LimitKind identifies one resource limit applied to managed containers.
type LimitKind string

const (
	// LimitCPU is the CPU quota reported through NanoCPUs.
	LimitCPU LimitKind = "cpu"
	// LimitMemory is the container memory limit.
	LimitMemory LimitKind = "memory"
	// LimitPIDs is the container process limit.
	LimitPIDs LimitKind = "pids"
	// LimitOpenFiles is the soft and hard nofile ulimit.
	LimitOpenFiles LimitKind = "open_files"
)

// LimitSupport reports whether the Docker host enforces one limit kind.
// Detail records the Docker capability that determined the result.
type LimitSupport struct {
	Kind      LimitKind
	Supported bool
	Detail    string
}

// HostLimits reports the Docker host and the resource limits it enforces.
// Limits contains one entry per LimitKind in CPU, memory, PIDs and open files
// order.
type HostLimits struct {
	OSType          string
	Architecture    string
	OperatingSystem string
	ServerVersion   string
	KernelVersion   string
	CgroupVersion   string
	CgroupDriver    string
	CPUs            int
	MemoryBytes     int64
	// SwapLimit reports whether Docker can bound swap for a memory-limited
	// container. Without swap limits, the memory cap does not bound the total
	// memory and swap usage.
	SwapLimit bool
	Limits    []LimitSupport
}

// Supports reports whether the host enforces kind. An unknown kind is
// unsupported.
func (h HostLimits) Supports(kind LimitKind) bool {
	for _, limit := range h.Limits {
		if limit.Kind == kind {
			return limit.Supported
		}
	}
	return false
}

// ProbeHostLimits reads the Docker Engine host information and reports which
// limits the adapter can rely on. A non-Linux engine returns fs.ErrInvalid
// because the adapter only manages Linux containers.
func ProbeHostLimits(ctx context.Context, docker *client.Client) (HostLimits, error) {
	if docker == nil {
		return HostLimits{}, fmt.Errorf("probe host limits: Docker client is required: %w", fs.ErrInvalid)
	}
	result, err := docker.Info(ctx, client.InfoOptions{})
	if err != nil {
		return HostLimits{}, fmt.Errorf("probe host limits: read Docker info: %w", err)
	}
	return classifyHostLimits(result.Info)
}

// classifyHostLimits reports the host scope from the Docker Engine information.
// Docker reports the controllers it can enforce, so the report follows those
// capabilities instead of inferring support from the kernel version alone.
func classifyHostLimits(info system.Info) (HostLimits, error) {
	if info.OSType != "linux" {
		return HostLimits{}, fmt.Errorf("probe host limits: Docker Engine provides %q containers, want linux: %w", info.OSType, fs.ErrInvalid)
	}
	return HostLimits{
		OSType:          info.OSType,
		Architecture:    info.Architecture,
		OperatingSystem: info.OperatingSystem,
		ServerVersion:   info.ServerVersion,
		KernelVersion:   info.KernelVersion,
		CgroupVersion:   info.CgroupVersion,
		CgroupDriver:    info.CgroupDriver,
		CPUs:            info.NCPU,
		MemoryBytes:     info.MemTotal,
		SwapLimit:       info.SwapLimit,
		Limits: []LimitSupport{
			{
				Kind:      LimitCPU,
				Supported: info.CPUCfsQuota && info.CPUCfsPeriod,
				Detail:    cpuLimitDetail(info),
			},
			{
				Kind:      LimitMemory,
				Supported: info.MemoryLimit,
				Detail:    memoryLimitDetail(info),
			},
			{
				Kind:      LimitPIDs,
				Supported: info.PidsLimit,
				Detail:    pidsLimitDetail(info),
			},
			{
				Kind:      LimitOpenFiles,
				Supported: true,
				Detail:    "Linux containers support the nofile ulimit",
			},
		},
	}, nil
}

func cpuLimitDetail(info system.Info) string {
	switch {
	case info.CPUCfsQuota && info.CPUCfsPeriod:
		return "Docker reports CPU CFS quota and period support"
	case !info.CPUCfsQuota && !info.CPUCfsPeriod:
		return "Docker reports no CPU CFS quota or period support"
	case !info.CPUCfsQuota:
		return "Docker reports no CPU CFS quota support"
	default:
		return "Docker reports no CPU CFS period support"
	}
}

func memoryLimitDetail(info system.Info) string {
	if info.MemoryLimit {
		return fmt.Sprintf("Docker reports memory limit support (swap limit: %t)", info.SwapLimit)
	}
	return "Docker reports no memory limit support"
}

func pidsLimitDetail(info system.Info) string {
	if info.PidsLimit {
		return "Docker reports pids limit support"
	}
	return "Docker reports no pids limit support"
}
