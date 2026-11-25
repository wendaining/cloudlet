//go:build integration

package database_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
)

func TestMCPReadOnlyQueriesUseAuthenticatedProjectAccess(t *testing.T) {
	ctx := context.Background()
	imagesFile, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images map[string]string
	if err := json.Unmarshal(imagesFile, &images); err != nil {
		t.Fatal(err)
	}
	container, err := postgres.Run(ctx, images["postgres"],
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("mcp-integration-database-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			if err := container.Terminate(ctx); err != nil {
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
	t.Cleanup(func() {
		if err := store.SQL.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const ownerEmail, ownerPassword = "mcp-owner@cloudlet.local", "mcp-owner-integration-password"
	const otherEmail, otherPassword = "mcp-other@cloudlet.local", "mcp-other-integration-password"
	if err := store.SeedUser(ctx, ownerEmail, ownerPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, otherEmail, otherPassword); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, ownerEmail, ownerPassword)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Authenticate(ctx, otherEmail, otherPassword)
	if err != nil {
		t.Fatal(err)
	}
	ownerProject, err := store.CreateProject(ctx, owner.ID, "MCP owner project")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := store.CreateProject(ctx, other.ID, "MCP other project")
	if err != nil {
		t.Fatal(err)
	}
	operationStore, err := operations.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	op, _, err := operationStore.Register(ctx, operations.Registration{
		ProjectID: uuid.MustParse(ownerProject.ID), Action: "mcp-test", IdempotencyKey: "mcp-operation", RequestFingerprint: "mcp-operation",
	})
	if err != nil {
		t.Fatal(err)
	}

	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "management API must bypass the proxy", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	listener, err := net.Listen("tcp4", net.JoinHostPort(nonLoopbackIPv4(t), "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	server.Listener.Close()
	server.Listener = listener
	endpoint := "http://" + listener.Addr().String()
	handler, closeSessions, err := controlplane.NewHandlerWithMCP(store, nil, nil,
		config.Config{Instance: "mcp-integration", PublicOrigin: "http://localhost"}, slog.Default(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(func() { server.Close(); closeSessions() })

	login := func(email, password string) *http.Client {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{
			Jar:       jar,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		body, err := json.Marshal(map[string]string{"email": email, "password": password})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/auth/login", strings.NewReader(string(body)))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("login returned HTTP %d", response.StatusCode)
		}
		return client
	}
	ownerHTTP, otherHTTP := login(ownerEmail, ownerPassword), login(otherEmail, otherPassword)
	newSession := func(source *http.Client) *mcp.ClientSession {
		clientHTTP := &http.Client{
			Jar:       source.Jar,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "cloudlet-test-client", Version: "1.0.0"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: clientHTTP, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := session.Close(); err != nil {
				t.Errorf("close MCP session: %v", err)
			}
		})
		return session
	}
	ownerSession, otherSession := newSession(ownerHTTP), newSession(otherHTTP)
	call := func(session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("MCP %s request failed: %v", name, err)
		}
		return result
	}
	decode := func(result *mcp.CallToolResult, target any) {
		t.Helper()
		if result.IsError {
			t.Fatalf("MCP query denied unexpectedly: %+v", result.Content)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, target); err != nil {
			t.Fatalf("decode MCP result %s: %v", encoded, err)
		}
	}

	for _, tc := range []struct {
		session *mcp.ClientSession
		project database.Project
	}{{ownerSession, ownerProject}, {otherSession, otherProject}} {
		var listed struct {
			Projects []apigen.Project `json:"projects"`
		}
		decode(call(tc.session, "list_projects", map[string]any{}), &listed)
		if len(listed.Projects) != 1 || listed.Projects[0].Id.String() != tc.project.ID {
			t.Fatalf("list_projects leaked or omitted projects: %+v", listed)
		}
	}
	projectPath := server.URL + "/api/v1/projects/" + ownerProject.ID
	expectedProject := decodeProject(t, memberRequest(t, ownerHTTP, http.MethodGet, projectPath, nil, http.StatusOK))
	var gotProject apigen.Project
	decode(call(ownerSession, "get_project", map[string]any{"project_id": ownerProject.ID}), &gotProject)
	if gotProject != expectedProject || gotProject.Id.String() != ownerProject.ID {
		t.Fatalf("get_project returned %+v, management API returned %+v", gotProject, expectedProject)
	}

	response := memberRequest(t, ownerHTTP, http.MethodGet, projectPath+"/operations", nil, http.StatusOK)
	defer response.Body.Close()
	var expectedOperations []apigen.Operation
	if err := json.NewDecoder(response.Body).Decode(&expectedOperations); err != nil {
		t.Fatal(err)
	}
	if len(expectedOperations) != 1 || expectedOperations[0].Id != op.ID {
		t.Fatalf("management API returned unexpected operations: %+v", expectedOperations)
	}
	var listedOps struct {
		Operations []apigen.Operation `json:"operations"`
	}
	decode(call(ownerSession, "list_project_operations", map[string]any{"project_id": ownerProject.ID}), &listedOps)
	if len(listedOps.Operations) != 1 || listedOps.Operations[0] != expectedOperations[0] {
		t.Fatalf("list_project_operations returned %+v, management API returned %+v", listedOps, expectedOperations)
	}
	var gotOperation apigen.Operation
	decode(call(ownerSession, "get_operation", map[string]any{"project_id": ownerProject.ID, "operation_id": strings.ToUpper(op.ID.String())}), &gotOperation)
	if gotOperation != expectedOperations[0] {
		t.Fatalf("get_operation returned %+v, management API returned %+v", gotOperation, expectedOperations[0])
	}

	for _, tc := range []struct {
		session   *mcp.ClientSession
		projectID string
	}{{ownerSession, otherProject.ID}, {otherSession, ownerProject.ID}} {
		if result := call(tc.session, "get_project", map[string]any{"project_id": tc.projectID}); !result.IsError {
			t.Fatalf("caller read project %s without membership", tc.projectID)
		}
		if result := call(tc.session, "list_project_operations", map[string]any{"project_id": tc.projectID}); !result.IsError {
			t.Fatalf("caller listed operations for project %s without membership", tc.projectID)
		}
		if result := call(tc.session, "get_operation", map[string]any{"project_id": tc.projectID, "operation_id": uuid.NewString()}); !result.IsError {
			t.Fatalf("caller queried operation in project %s without membership", tc.projectID)
		}
	}
	anonymous := newSession(&http.Client{})
	if result := call(anonymous, "list_projects", map[string]any{}); !result.IsError {
		t.Fatal("unauthenticated MCP query succeeded")
	}
	if hits := proxyHits.Load(); hits != 0 {
		t.Fatalf("MCP management API request used HTTP proxy %d times", hits)
	}
}

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, networkInterface := range interfaces {
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return ""
}
