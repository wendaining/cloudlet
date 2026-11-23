//go:build integration

package workers_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/wendaining/cloudlet/internal/workers"
)

func TestWorkerBuildRunsInWorkerd(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root := repositoryRoot(t)
	workerRoot := filepath.Join(root, "runtimes", "workers")
	outputDir := t.TempDir()
	first := filepath.Join(outputDir, "first.mjs")
	second := filepath.Join(outputDir, "second.mjs")

	runWorkerBuild(t, workerRoot, "--outfile", first)
	runWorkerBuild(t, workerRoot, "--outfile", second)
	firstOutput := readFile(t, first)
	secondOutput := readFile(t, second)
	if sha256.Sum256(firstOutput) != sha256.Sum256(secondOutput) {
		t.Fatal("repeated builds produced different module bytes")
	}

	prepared, err := workers.Prepare(filepath.Dir(first), filepath.Base(first), compatibilityDate)
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(workers.HTTPPort) + "/tcp"
	container := runWorkerd(t, ctx, prepared,
		testcontainers.WithExposedPorts(port),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/ready").WithPort(port)),
	)
	endpoint, err := container.PortEndpoint(ctx, port, "http")
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Worker") != "cloudlet" || string(body) != "Hello /hello" {
		t.Fatalf("built Worker response: status=%d, X-Worker=%q, body=%q", resp.StatusCode, resp.Header.Get("X-Worker"), body)
	}
	t.Logf("workerd %s served the repeated TypeScript build with sha256 %x", workers.RuntimeVersion, sha256.Sum256(firstOutput))
}

func TestWorkerBuildReportsTypeErrors(t *testing.T) {
	root := repositoryRoot(t)
	workerRoot := filepath.Join(root, "runtimes", "workers")
	cmd := exec.Command("npm", "run", "build", "--",
		"--entry", "testdata/type-error/index.ts",
		"--tsconfig", "testdata/type-error/tsconfig.json",
		"--outfile", filepath.Join(t.TempDir(), "index.mjs"),
	)
	cmd.Dir = workerRoot
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("build accepted a TypeScript type error")
	}
	if !bytes.Contains(output, []byte("TS2322")) || !bytes.Contains(output, []byte("Type 'number' is not assignable to type 'string'")) {
		t.Fatalf("build output does not contain the type diagnostic:\n%s", output)
	}
}

func runWorkerBuild(t *testing.T, workerRoot string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("npm", append([]string{"run", "build", "--"}, args...)...)
	cmd.Dir = workerRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("npm run build: %v\n%s", err, output)
	}
	return output
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	content, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root %q does not contain go.mod: %v", root, err)
	}
	return root
}
