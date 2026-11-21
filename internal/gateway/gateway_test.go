package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/version"
	"github.com/wendaining/cloudlet/pkg/client"
)

func TestGatewayAndClientAgainstManagementAPI(t *testing.T) {
	management, cleanup := controlplane.NewHandler(nil, config.Config{}, slog.Default())
	defer cleanup()
	api := httptest.NewServer(management)
	defer api.Close()
	handler, err := gateway.NewHandler(config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: "demo.localhost", Target: api.URL}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(handler)
	defer front.Close()
	for _, host := range []string{"demo.localhost", "DEMO.localhost.:8081"} {
		request, err := http.NewRequest(http.MethodGet, front.URL+"/api/v1/version?check=1", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := front.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		var info version.Info
		decodeError := json.NewDecoder(response.Body).Decode(&info)
		response.Body.Close()
		if decodeError != nil || response.StatusCode != http.StatusOK || info != version.Current() {
			t.Fatalf("unexpected API response: status=%d version=%+v error=%v", response.StatusCode, info, decodeError)
		}
	}
	response, err := front.Client().Get(front.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown host returned %d", response.StatusCode)
	}
	cli, err := client.New(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.Close()
	request, err := http.NewRequest(http.MethodGet, front.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "demo.localhost"
	response, err = front.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("stopped upstream returned %d", response.StatusCode)
	}
}

func TestGatewayForwardsFileAndRangeRequests(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "asset.txt"), []byte("cloudlet gateway integration"), 0o600); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.FileServer(http.Dir(directory)))
	defer origin.Close()
	handler, err := gateway.NewHandler(config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: "files.localhost", Target: origin.URL}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(handler)
	defer front.Close()
	request, err := http.NewRequest(http.MethodGet, front.URL+"/asset.txt?download=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "files.localhost"
	request.Header.Set("Range", "bytes=0-7")
	response, err := front.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || string(body) != "cloudlet" {
		t.Fatalf("range request failed: status=%d body=%q", response.StatusCode, body)
	}
}

func TestGatewayReplacesDynamicRoutesWhileStreaming(t *testing.T) {
	const dynamicHost = "worker.localhost"
	const staticHost = "static.localhost"
	release := make(chan struct{})
	versionA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: A started\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			fmt.Fprint(w, "data: A finished\n\n")
		case <-r.Context().Done():
		}
	}))
	defer versionA.Close()
	versionB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "B")
	}))
	defer versionB.Close()

	handler, err := gateway.NewHandler(config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: staticHost, Target: versionB.URL}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(handler)
	defer front.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	get := func(host string) *http.Response {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := front.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	assertRoute := func(host string, status int, body string) {
		t.Helper()
		response := get(host)
		defer response.Body.Close()
		got, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || string(got) != body {
			t.Fatalf("request to %s: status=%d body=%q error=%v", host, response.StatusCode, got, err)
		}
	}
	if err := handler.ReplaceRoutes([]config.Route{{Host: dynamicHost, Target: versionA.URL}}); err != nil {
		t.Fatal(err)
	}
	responseA := get(dynamicHost)
	defer responseA.Body.Close()
	const start = "data: A started\n\n"
	chunk := make([]byte, len(start))
	if _, err := io.ReadFull(responseA.Body, chunk); err != nil || responseA.StatusCode != http.StatusOK || string(chunk) != start {
		t.Fatalf("initial stream: status=%d body=%q error=%v", responseA.StatusCode, chunk, err)
	}
	if err := handler.ReplaceRoutes([]config.Route{{Host: dynamicHost, Target: versionB.URL}}); err != nil {
		t.Fatal(err)
	}
	assertRoute("WORKER.localhost.:8081", http.StatusOK, "B")
	close(release)
	if rest, err := io.ReadAll(responseA.Body); err != nil || string(rest) != "data: A finished\n\n" {
		t.Fatalf("in-flight stream after replacement: body=%q error=%v", rest, err)
	}

	for _, invalid := range []config.Route{
		{Host: "broken.localhost", Target: "ftp://example.com"},
		{Host: "STATIC.localhost.", Target: versionA.URL},
		{Host: "WORKER.localhost.", Target: versionA.URL},
	} {
		if err := handler.ReplaceRoutes([]config.Route{{Host: dynamicHost, Target: versionA.URL}, invalid}); err == nil {
			t.Fatalf("invalid replacement was accepted: %+v", invalid)
		}
		assertRoute(dynamicHost, http.StatusOK, "B")
		assertRoute(staticHost, http.StatusOK, "B")
	}
	if err := handler.ReplaceRoutes(nil); err != nil {
		t.Fatal(err)
	}
	assertRoute(dynamicHost, http.StatusNotFound, "404 page not found\n")
	assertRoute(staticHost, http.StatusOK, "B")
}

func TestClientRejectsUnreachableAPI(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + listener.Addr().String()
	listener.Close()
	api, err := client.New(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.Health(context.Background()); err == nil {
		t.Fatal("unreachable API was accepted")
	}
}
