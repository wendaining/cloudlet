//go:build integration

package workers_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/workers"
)

func TestWorkerPreflightReportsRuntimePhase(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })

	for _, tt := range []struct {
		name        string
		module      string
		phase       workers.PreflightPhase
		failed      bool
		logContains string
	}{
		{
			name:   "ready",
			module: `export default { fetch() { return new Response("ready") } };`,
			phase:  workers.PreflightReady,
		},
		{
			name:        "syntax error",
			module:      `export default { fetch() { return new Response( } };`,
			phase:       workers.PreflightLoad,
			failed:      true,
			logContains: "Uncaught SyntaxError",
		},
		{
			name:        "initialization error",
			module:      `throw new Error("initialization failed"); export default { fetch() { return new Response("ready") } };`,
			phase:       workers.PreflightInitialize,
			failed:      true,
			logContains: "initialization failed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := "dist/index.mjs"
			prepared, err := workers.Prepare(writeBundle(t, entry, []byte(tt.module)), entry, compatibilityDate)
			if err != nil {
				t.Fatal(err)
			}
			result, err := workers.Preflight(ctx, docker, prepared)
			if result.Phase != tt.phase {
				t.Fatalf("phase = %s, want %s, error = %v, logs = %s", result.Phase, tt.phase, err, result.Logs)
			}
			if !tt.failed {
				if err != nil {
					t.Fatalf("Preflight error = %v, logs = %s", err, result.Logs)
				}
				return
			}
			var preflightErr *workers.PreflightError
			if !errors.As(err, &preflightErr) || preflightErr.Phase != tt.phase {
				t.Fatalf("Preflight error = %v, want phase %s", err, tt.phase)
			}
			if tt.logContains != "" && !strings.Contains(preflightErr.Logs, tt.logContains) {
				t.Fatalf("preflight logs do not contain %q:\n%s", tt.logContains, preflightErr.Logs)
			}
		})
	}
}
