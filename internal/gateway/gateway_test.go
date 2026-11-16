package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/version"
	"github.com/wendaining/cloudlet/pkg/client"
)

func TestGatewayAndClientAgainstManagementAPI(t *testing.T) {
	api := httptest.NewServer(controlplane.NewHandler())
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
