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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
)

func TestMCPBrowserSessionAndNavigation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	runtimeDir, imageID := browserRuntimeImage(t, ctx)
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const password = "mcp-browser-integration-password"
	emails := []string{
		"mcp-browser-owner@cloudlet.local",
		"mcp-browser-other@cloudlet.local",
		"mcp-browser-viewer@cloudlet.local",
	}
	owner, other := makeProjectOwners(t, store, emails[:2], password)
	_ = other
	if err := store.SeedUser(ctx, emails[2], password); err != nil {
		t.Fatal(err)
	}
	viewer, err := store.Authenticate(ctx, emails[2], password)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, owner.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}

	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()

	const instance = "mcp-browser-integration"
	screenshotDir := filepath.Join(t.TempDir(), "screenshots")
	service, err := browser.NewService(ctx, store, docker, instance, imageID, filepath.Join(runtimeDir, "seccomp.json"), screenshotDir, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	}()

	fixture := chromiumPage(t, ctx, docker)

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
	handler, closeSessions, err := controlplane.NewHandlerWithMCP(store, nil, service,
		config.Config{Instance: instance, PublicOrigin: "http://localhost"}, slog.Default(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(func() { server.Close(); closeSessions() })

	login := func(email string) *http.Client {
		t.Helper()
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		httpClient := &http.Client{
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
		response, err := httpClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("login %s returned HTTP %d", email, response.StatusCode)
		}
		return httpClient
	}

	ownerHTTP := login(emails[0])
	otherHTTP := login(emails[1])
	viewerHTTP := login(emails[2])

	newMCPSession := func(source *http.Client) *mcp.ClientSession {
		t.Helper()
		clientHTTP := &http.Client{
			Jar:       source.Jar,
			Transport: &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		mcpClient := mcp.NewClient(&mcp.Implementation{Name: "cloudlet-browser-test-client", Version: "1.0.0"}, nil)
		session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:             server.URL + "/mcp",
			HTTPClient:           clientHTTP,
			DisableStandaloneSSE: true,
		}, nil)
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

	ownerSession := newMCPSession(ownerHTTP)
	otherSession := newMCPSession(otherHTTP)
	viewerSession := newMCPSession(viewerHTTP)
	anonymousSession := newMCPSession(&http.Client{})

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
			t.Fatalf("MCP tool returned unexpected error: %+v", result.Content)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(encoded, target); err != nil {
			t.Fatalf("decode MCP result %s: %v", encoded, err)
		}
	}

	var created struct {
		ID            string `json:"id"`
		Endpoint      string `json:"endpoint"`
		ExpiresAt     string `json:"expires_at"`
		Status        string `json:"status"`
		DesiredStatus string `json:"desired_status"`
		ActualStatus  string `json:"actual_status"`
	}
	decode(call(ownerSession, "create_browser_session", map[string]any{"project_id": owner.ID}), &created)
	sessionID, err := uuid.Parse(created.ID)
	if err != nil || sessionID == uuid.Nil {
		t.Fatalf("create_browser_session returned invalid id %q: %v", created.ID, err)
	}
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := service.CloseSession(cleanupCtx, uuid.MustParse(owner.ID), sessionID); err != nil {
			t.Errorf("close browser session %s: %v", sessionID, err)
		}
	})

	wantEndpoint := "/api/v1/projects/" + owner.ID + "/browser-sessions/" + sessionID.String()
	if created.Endpoint != wantEndpoint || created.Status != "ready" || created.DesiredStatus != "ready" || created.ActualStatus != "ready" {
		t.Fatalf("unexpected created browser session: %+v", created)
	}
	t.Logf("MCP create_browser_session: id=%s status=%s endpoint=%s", created.ID, created.Status, created.Endpoint)

	targetURL := fixture + "?visible-text"
	var navigated struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	decode(call(ownerSession, "navigate_browser_session", map[string]any{
		"project_id": owner.ID,
		"session_id": created.ID,
		"url":        targetURL,
	}), &navigated)
	if navigated.URL != targetURL || navigated.Title != "Cloudlet Browser Run" {
		t.Fatalf("navigate_browser_session = %+v, want url=%q title=%q", navigated, targetURL, "Cloudlet Browser Run")
	}
	t.Logf("MCP navigate_browser_session: url=%s title=%q", navigated.URL, navigated.Title)

	for _, tc := range []struct {
		label   string
		session *mcp.ClientSession
	}{
		{"non-member", otherSession},
		{"viewer", viewerSession},
		{"anonymous", anonymousSession},
	} {
		if result := call(tc.session, "create_browser_session", map[string]any{"project_id": owner.ID}); !result.IsError {
			t.Fatalf("%s unexpectedly created browser session in project %s", tc.label, owner.ID)
		}
		if result := call(tc.session, "navigate_browser_session", map[string]any{
			"project_id": owner.ID,
			"session_id": created.ID,
			"url":        targetURL,
		}); !result.IsError {
			t.Fatalf("%s unexpectedly navigated browser session %s in project %s", tc.label, created.ID, owner.ID)
		}
	}

	if hits := proxyHits.Load(); hits != 0 {
		t.Fatalf("MCP management API request used HTTP proxy %d times", hits)
	}
}
