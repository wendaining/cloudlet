//go:build integration

package containers_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/containers"
)

func TestAdapterWaitReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	docker, adapter := readinessAdapter(t, ctx)
	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	ownership := readinessOwnership("ready")
	spec := readinessSpec(ownership, image, []string{"sh", "-c", `sleep 3; exec nginx -g "daemon off;"`})

	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cleanupReadinessContainer(t, docker, adapter, ownership, created.ID)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	port := waitForPort(t, ctx, adapter, ownership, created.ID, spec.Port)

	probe := containers.ReadinessProbe{
		Port:     spec.Port,
		Path:     "/",
		Timeout:  30 * time.Second,
		Interval: 100 * time.Millisecond,
		Statuses: []int{http.StatusOK},
	}
	readiness, err := adapter.WaitReady(ctx, ownership, created.ID, probe)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready {
		t.Fatalf("readiness = %+v, want ready", readiness)
	}
	if readiness.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", readiness.StatusCode)
	}
	if readiness.Attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2 while the service starts", readiness.Attempts)
	}
	if readiness.Elapsed < time.Second {
		t.Fatalf("elapsed = %s, want the probe to wait for the delayed service", readiness.Elapsed)
	}
	if want := "http://" + port.HostAddress() + "/"; readiness.Endpoint != want {
		t.Fatalf("endpoint = %q, want %q", readiness.Endpoint, want)
	}
	if readiness.Observation.ID != created.ID || !readiness.Observation.Running {
		t.Fatalf("observation = %+v, want the running created container", readiness.Observation)
	}

	other := ownership
	other.Project = "project-other"
	if _, err := adapter.WaitReady(ctx, other, created.ID, probe); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("WaitReady() with mismatched ownership = %v, want ErrNotOwned", err)
	}
	if _, err := adapter.WaitReady(ctx, ownership, strings.Repeat("a", 12), probe); !errors.Is(err, containers.ErrNotFound) {
		t.Fatalf("WaitReady() for missing container = %v, want ErrNotFound", err)
	}

	wrongPort := probe
	wrongPort.Port = 9999
	wrongPort.Timeout = 300 * time.Millisecond
	start := time.Now()
	readiness, err = adapter.WaitReady(ctx, ownership, created.ID, wrongPort)
	elapsed := time.Since(start)
	if !errors.Is(err, containers.ErrNotReady) {
		t.Fatalf("WaitReady() with unpublished port = %v, want ErrNotReady", err)
	}
	if readiness.Attempts != 0 || readiness.StatusCode != 0 {
		t.Fatalf("readiness = %+v, want no HTTP attempts", readiness)
	}
	if elapsed < wrongPort.Timeout {
		t.Fatalf("elapsed = %s, want at least %s", elapsed, wrongPort.Timeout)
	}
}

func TestAdapterWaitReadyRejectsUnhealthyService(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	docker, adapter := readinessAdapter(t, ctx)
	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	ownership := readinessOwnership("unhealthy")
	command := `cat > /tmp/nginx.conf <<'EOF'
worker_processes 1;
pid /tmp/nginx.pid;
error_log /dev/stderr notice;
events { worker_connections 64; }
http {
  access_log /dev/stdout;
  client_body_temp_path /tmp/client_body;
  proxy_temp_path /tmp/proxy;
  fastcgi_temp_path /tmp/fastcgi;
  uwsgi_temp_path /tmp/uwsgi;
  scgi_temp_path /tmp/scgi;
  server {
    listen 8080;
    location = /ok { return 200; }
    location / { return 503; }
  }
}
EOF
exec nginx -c /tmp/nginx.conf -g "daemon off;"`
	spec := readinessSpec(ownership, image, []string{"sh", "-c", command})

	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cleanupReadinessContainer(t, docker, adapter, ownership, created.ID)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}
	waitForPort(t, ctx, adapter, ownership, created.ID, spec.Port)

	probe := containers.ReadinessProbe{
		Port:     spec.Port,
		Path:     "/",
		Timeout:  2 * time.Second,
		Interval: 100 * time.Millisecond,
		Statuses: []int{http.StatusOK},
	}
	start := time.Now()
	readiness, err := adapter.WaitReady(ctx, ownership, created.ID, probe)
	elapsed := time.Since(start)
	if !errors.Is(err, containers.ErrNotReady) {
		t.Fatalf("WaitReady() = %v, want ErrNotReady", err)
	}
	if readiness.Ready {
		t.Fatalf("readiness = %+v, want not ready", readiness)
	}
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", readiness.StatusCode)
	}
	if readiness.Attempts < 2 {
		t.Fatalf("attempts = %d, want repeated attempts", readiness.Attempts)
	}
	if elapsed < probe.Timeout || elapsed > 8*time.Second {
		t.Fatalf("elapsed = %s, want a bounded probe around %s", elapsed, probe.Timeout)
	}
	if !readiness.Observation.Running {
		t.Fatalf("observation = %+v, want the container still running", readiness.Observation)
	}

	readyProbe := probe
	readyProbe.Path = "/ok"
	readyProbe.Timeout = 5 * time.Second
	readiness, err = adapter.WaitReady(ctx, ownership, created.ID, readyProbe)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.StatusCode != http.StatusOK {
		t.Fatalf("readiness on /ok = %+v, want ready with status 200", readiness)
	}
}

func TestAdapterWaitReadyFailsWhenContainerExits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	docker, adapter := readinessAdapter(t, ctx)
	image := httpImage(t)
	ensureImage(ctx, t, docker, image)
	ownership := readinessOwnership("exits")
	spec := readinessSpec(ownership, image, []string{"sh", "-c", "sleep 0.2; exit 7"})

	created, err := adapter.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	cleanupReadinessContainer(t, docker, adapter, ownership, created.ID)
	if _, err := adapter.Start(ctx, ownership, created.ID); err != nil {
		t.Fatal(err)
	}

	probe := containers.ReadinessProbe{
		Port:     spec.Port,
		Path:     "/",
		Timeout:  10 * time.Second,
		Interval: 50 * time.Millisecond,
		Statuses: []int{http.StatusOK},
	}
	start := time.Now()
	readiness, err := adapter.WaitReady(ctx, ownership, created.ID, probe)
	elapsed := time.Since(start)
	if !errors.Is(err, containers.ErrNotReady) {
		t.Fatalf("WaitReady() = %v, want ErrNotReady", err)
	}
	if readiness.Observation.Running {
		t.Fatalf("observation = %+v, want the exited container", readiness.Observation)
	}
	if readiness.Observation.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", readiness.Observation.ExitCode)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want the probe to stop when the container exits", elapsed)
	}
}

func readinessAdapter(t *testing.T, ctx context.Context) (*client.Client, *containers.Adapter) {
	t.Helper()
	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	return docker, adapter
}

func readinessOwnership(resource string) containers.Ownership {
	return containers.Ownership{
		Environment: "integration",
		Project:     "project-" + rand.Text(),
		Resource:    resource + "-" + rand.Text(),
		Generation:  1,
	}
}

func readinessSpec(ownership containers.Ownership, image string, command []string) containers.Spec {
	return containers.Spec{
		Ownership: ownership,
		Image:     image,
		User:      "101",
		Command:   command,
		Port:      8080,
		Access:    containers.AccessPublished,
		Limits: containers.Limits{
			CPUMillis:   500,
			MemoryBytes: 128 << 20,
			PIDs:        64,
			OpenFiles:   1024,
		},
	}
}

func cleanupReadinessContainer(t *testing.T, docker *client.Client, adapter *containers.Adapter, ownership containers.Ownership, id string) {
	t.Helper()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		if err := adapter.Stop(cleanupCtx, ownership, id); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup stop: %v", err)
		}
		if err := adapter.Remove(cleanupCtx, ownership, id); err != nil && !errors.Is(err, containers.ErrNotFound) {
			t.Errorf("cleanup remove: %v", err)
		}
		if _, err := docker.ContainerInspect(cleanupCtx, id, client.ContainerInspectOptions{}); err == nil {
			_, _ = docker.ContainerRemove(cleanupCtx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
	})
}
