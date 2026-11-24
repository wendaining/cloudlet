package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
)

// Exercise the public listeners of the built server against actual site files.
func TestGatewayDomainRoutingAcceptance(t *testing.T) {
	directory := t.TempDir()
	const content = "cloudlet domain routing acceptance\n"
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.FileServer(http.Dir(directory)))
	t.Cleanup(origin.Close)
	healthy := httptest.NewServer(http.FileServer(http.Dir(directory)))
	t.Cleanup(healthy.Close)
	address := func() string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		value := listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return value
	}
	cfg := config.Config{
		APIAddress: address(), GatewayAddress: address(),
		Routes: []config.Route{
			{Host: "demo.localhost", Target: origin.URL},
			{Host: "healthy.localhost", Target: healthy.URL},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "cloudlet-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	var logs bytes.Buffer
	process := exec.Command(binary, "-config", configPath)
	process.Stdout, process.Stderr = &logs, &logs
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- process.Wait() }()
	t.Cleanup(func() {
		if err := process.Process.Signal(os.Interrupt); err != nil {
			t.Error(err)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Errorf("server exit: %v\n%s", err, logs.String())
			}
		case <-time.After(15 * time.Second):
			if err := process.Process.Kill(); err != nil {
				t.Error(err)
			}
			<-exited
			t.Error("server did not stop")
		}
		if !strings.Contains(logs.String(), "proxy request failed") || !strings.Contains(logs.String(), "demo.localhost") {
			t.Errorf("upstream failure diagnostic missing: %s", logs.String())
		}
	})
	client := &http.Client{Timeout: 2 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
ready:
	for {
		select {
		case <-deadline.C:
			t.Fatal("server readiness timed out")
		case <-ticker.C:
			response, err := client.Get("http://" + cfg.APIAddress + "/readyz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					break ready
				}
			}
		}
	}
	check := func(host, method string, status int, want string) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), method, "http://"+cfg.GatewayAddress+"/index.html?source=acceptance", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || string(body) != want {
			t.Fatalf("%s %s: status=%d body=%q error=%v", method, host, response.StatusCode, body, err)
		}
		t.Logf("%s %s: status=%d body=%q", method, host, response.StatusCode, body)
	}
	check("demo.localhost", http.MethodGet, http.StatusOK, content)
	check("DEMO.localhost.:8081", http.MethodGet, http.StatusOK, content)
	check("demo.localhost", http.MethodHead, http.StatusOK, "")
	check("unknown.localhost", http.MethodGet, http.StatusNotFound, "404 page not found\n")
	origin.Close()
	check("demo.localhost", http.MethodGet, http.StatusBadGateway, "upstream unavailable\n")
	check("unknown.localhost", http.MethodGet, http.StatusNotFound, "404 page not found\n")
	check("healthy.localhost", http.MethodGet, http.StatusOK, content)
}
