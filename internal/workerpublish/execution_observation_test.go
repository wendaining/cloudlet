package workerpublish

import (
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/containers"
)

func TestExecutionExitReason(t *testing.T) {
	finished := time.Date(2026, 11, 25, 12, 0, 0, 123456789, time.UTC)
	for _, test := range []struct {
		name     string
		observed containers.Observation
		missing  bool
		reason   string
		code     bool
		detail   string
	}{
		{name: "normal", observed: containers.Observation{FinishedAt: finished}, reason: "normal_exit", code: true},
		{name: "signal", observed: containers.Observation{FinishedAt: finished, ExitCode: 143}, reason: "nonzero_exit", code: true},
		{name: "memory limit", observed: containers.Observation{FinishedAt: finished, ExitCode: 137, OOMKilled: true}, reason: "oom_killed", code: true},
		{name: "engine error", observed: containers.Observation{StateError: "cannot start task"}, reason: "docker_error", detail: "cannot start task"},
		{name: "not started", observed: containers.Observation{Status: "created"}, reason: "not_running", detail: "created"},
		{name: "missing", missing: true, reason: "container_missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			exit := classifyExecutionExit(test.observed, test.missing)
			if exit.Reason != test.reason || (exit.Code != nil) != test.code || exit.Detail != test.detail {
				t.Fatalf("exit report = %+v", exit)
			}
			if test.code && (*exit.Code != test.observed.ExitCode || exit.FinishedAt == nil || !exit.FinishedAt.Equal(finished.Truncate(time.Microsecond))) {
				t.Fatalf("exit facts = %+v", exit)
			}
		})
	}
}
