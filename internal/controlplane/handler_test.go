package controlplane_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
)

func TestProjectCapacityRequiresDatabase(t *testing.T) {
	handler, cleanup := controlplane.NewHandler(nil, config.Config{}, slog.Default())
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/api/v1/projects/" + uuid.NewString() + "/capacity")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置数据库返回 %d，要求 %d", response.StatusCode, http.StatusServiceUnavailable)
	}
}

func TestProjectCapacityUpdateRequiresDatabase(t *testing.T) {
	handler, cleanup := controlplane.NewHandler(nil, config.Config{}, slog.Default())
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPatch, server.URL+"/api/v1/projects/"+uuid.NewString()+"/capacity", strings.NewReader(`{"cpu_millis":1000,"memory_bytes":1073741824,"instances":1}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置数据库调整额度返回 %d，要求 %d", response.StatusCode, http.StatusServiceUnavailable)
	}
}
