//go:build integration

package workers_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wendaining/cloudlet/internal/workers"
)

const echoWorker = `export default {
  async fetch(request) {
    const url = new URL(request.url);
    return new Response(request.method + " " + url.pathname + " " + await request.text(), {
      headers: { "X-Worker": "workerd" }
    });
  }
};`

func TestWorkerdServesWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	prepared := prepareWorker(t)
	port := strconv.Itoa(workers.HTTPPort) + "/tcp"
	container := runWorkerd(t, ctx, prepared,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ready").WithPort(port)),
	)
	endpoint, err := container.PortEndpoint(ctx, port, "http")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/echo", strings.NewReader("cloudlet"))
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Worker") != "workerd" || string(body) != "POST /echo cloudlet" {
		t.Fatalf("fetch response: status=%d, X-Worker=%q, body=%q", resp.StatusCode, resp.Header.Get("X-Worker"), body)
	}
	t.Logf("workerd %s: HTTP %d, X-Worker=%s, body=%q", workers.RuntimeVersion, resp.StatusCode, resp.Header.Get("X-Worker"), body)
}

func TestWorkerdRejectsInvalidConfig(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	prepared := prepareWorker(t)
	prepared.Config = bytes.Replace(prepared.Config, []byte(compatibilityDate), []byte("2099-01-01"), 1)
	container := runWorkerd(t, ctx, prepared, testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(10*time.Second)))
	state, err := container.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := container.Logs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	output, err := io.ReadAll(logs)
	if err != nil {
		t.Fatal(err)
	}
	if state.ExitCode == 0 || !bytes.Contains(output, []byte("compatibility date")) || !bytes.Contains(output, []byte("2099-01-01")) {
		t.Fatalf("invalid config: exit code=%d, workerd output=%s", state.ExitCode, output)
	}
	t.Logf("workerd exit code=%d, output=%s", state.ExitCode, output)
}

func prepareWorker(t *testing.T) workers.RuntimeConfig {
	t.Helper()
	entry := "dist/index.mjs"
	prepared, err := workers.Prepare(writeBundle(t, entry, []byte(echoWorker)), entry, compatibilityDate)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func runWorkerd(t *testing.T, ctx context.Context, prepared workers.RuntimeConfig, options ...testcontainers.ContainerCustomizer) *testcontainers.DockerContainer {
	t.Helper()
	options = append(options,
		testcontainers.WithCmd(workers.ServeArgs()...),
		testcontainers.WithFiles(
			testcontainers.ContainerFile{Reader: bytes.NewReader(prepared.Config), ContainerFilePath: workers.ConfigPath, FileMode: 0444},
			testcontainers.ContainerFile{Reader: bytes.NewReader(prepared.Module), ContainerFilePath: workers.ModulePath, FileMode: 0444},
		),
	)
	container, err := testcontainers.Run(ctx, workers.RuntimeImage, options...)
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatalf("start %s: %v", workers.RuntimeImage, err)
	}
	return container
}
