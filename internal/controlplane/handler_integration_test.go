//go:build integration

package controlplane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestProjectCapacityQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	container, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("capacity-api-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
			defer cleanupCancel()
			if err := container.Terminate(cleanupCtx); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connection, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.SQL.Close() })
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}

	const ownerEmail = "capacity-owner@cloudlet.local"
	const viewerEmail = "capacity-viewer@cloudlet.local"
	const outsiderEmail = "capacity-outsider@cloudlet.local"
	const password = "capacity-api-integration-password"
	for _, email := range []string{ownerEmail, viewerEmail, outsiderEmail} {
		if err := store.SeedUser(ctx, email, password); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := store.Authenticate(ctx, ownerEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Authenticate(ctx, viewerEmail, password)
	if err != nil {
		t.Fatal(err)
	}

	projectA := createProject(t, ctx, store, owner.ID, "容量查询项目 A")
	projectB := createProject(t, ctx, store, owner.ID, "容量查询项目 B")
	projectWithoutQuota := createProject(t, ctx, store, owner.ID, "未配置配额项目")
	if err := store.UpsertProjectMember(ctx, projectA.String(), viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}

	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	hostID := "capacity-api-" + uuid.NewString()
	hostCapacity := capacity.Resources{CPUMillis: 8000, MemoryBytes: 8 << 30, Instances: 8}
	if _, err := capacityStore.SetHostCapacity(ctx, hostID, hostCapacity); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityStore.SetProjectQuota(ctx, projectA, capacity.Resources{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}); err != nil {
		t.Fatal(err)
	}
	if _, err := capacityStore.SetProjectQuota(ctx, projectB, capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 2}); err != nil {
		t.Fatal(err)
	}

	first, _, err := capacityStore.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectA,
		Resources: capacity.Resources{CPUMillis: 1000, MemoryBytes: 1 << 30, Instances: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := capacityStore.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectA,
		Resources: capacity.Resources{CPUMillis: 2000, MemoryBytes: 2 << 30, Instances: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := capacityStore.Reserve(ctx, capacity.Request{
		ExecutionID: uuid.New(), HostID: hostID, ProjectID: projectB,
		Resources: capacity.Resources{CPUMillis: 500, MemoryBytes: 512 << 20, Instances: 1},
	}); err != nil {
		t.Fatal(err)
	}

	handler, cleanup := controlplane.NewHandler(store, config.Config{
		Instance: "integration", PublicOrigin: "http://localhost",
	}, slog.Default())
	defer cleanup()
	server := httptest.NewServer(handler)
	defer server.Close()

	unauthenticated := &http.Client{Timeout: 10 * time.Second}
	requestJSON(t, unauthenticated, http.MethodGet, server.URL+"/api/v1/projects/"+projectA.String()+"/capacity", nil, http.StatusUnauthorized, nil)

	ownerClient := login(t, server, ownerEmail, password)
	viewerClient := login(t, server, viewerEmail, password)
	outsiderClient := login(t, server, outsiderEmail, password)

	expected := apigen.ProjectCapacity{
		ProjectId: projectA,
		Limit:     apigen.ResourceAmounts{CpuMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
		Used:      apigen.ResourceAmounts{CpuMillis: 3000, MemoryBytes: 3 << 30, Instances: 2},
		Remaining: apigen.ResourceAmounts{CpuMillis: 1000, MemoryBytes: 1 << 30, Instances: 2},
	}
	path := server.URL + "/api/v1/projects/" + projectA.String() + "/capacity"
	var actual apigen.ProjectCapacity
	requestJSON(t, ownerClient, http.MethodGet, path, nil, http.StatusOK, &actual)
	if actual != expected {
		t.Fatalf("项目容量返回 %+v，预期 %+v", actual, expected)
	}
	var viewerActual apigen.ProjectCapacity
	requestJSON(t, viewerClient, http.MethodGet, path, nil, http.StatusOK, &viewerActual)
	if viewerActual != expected {
		t.Fatalf("查看者读取项目容量返回 %+v，预期 %+v", viewerActual, expected)
	}

	requestJSON(t, outsiderClient, http.MethodGet, path, nil, http.StatusNotFound, nil)
	requestJSON(t, ownerClient, http.MethodGet, server.URL+"/api/v1/projects/not-a-uuid/capacity", nil, http.StatusNotFound, nil)
	requestJSON(t, ownerClient, http.MethodGet, server.URL+"/api/v1/projects/"+uuid.NewString()+"/capacity", nil, http.StatusNotFound, nil)
	var missingQuota apigen.Error
	requestJSON(t, ownerClient, http.MethodGet, server.URL+"/api/v1/projects/"+projectWithoutQuota.String()+"/capacity", nil, http.StatusNotFound, &missingQuota)
	if missingQuota.Message != "项目配额未配置" {
		t.Fatalf("未配置配额返回 %+v", missingQuota)
	}

	if _, err := capacityStore.Release(ctx, projectA, first.ExecutionID); err != nil {
		t.Fatal(err)
	}
	expected.Used = apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 1}
	expected.Remaining = apigen.ResourceAmounts{CpuMillis: 2000, MemoryBytes: 2 << 30, Instances: 3}
	requestJSON(t, ownerClient, http.MethodGet, path, nil, http.StatusOK, &actual)
	if actual != expected {
		t.Fatalf("释放后项目容量返回 %+v，预期 %+v", actual, expected)
	}
}

func createProject(t *testing.T, ctx context.Context, store *database.Store, ownerID, name string) uuid.UUID {
	t.Helper()
	project, err := store.CreateProject(ctx, ownerID, name)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := uuid.Parse(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return projectID
}

func login(t *testing.T, server *httptest.Server, email, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	requestJSON(t, client, http.MethodPost, server.URL+"/api/v1/auth/login", map[string]string{
		"email": email, "password": password,
	}, http.StatusOK, nil)
	return client
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint string, body any, expected int, target any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		t.Fatalf("%s %s 返回 %d，要求 %d：%s", method, endpoint, response.StatusCode, expected, payload)
	}
	if target != nil {
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
}
