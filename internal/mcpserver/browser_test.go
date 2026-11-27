package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestMapBrowserSessionFormatsFields(t *testing.T) {
	sessionID := uuid.New()
	expiresAt := time.Date(2026, 11, 28, 3, 4, 5, 123456789, time.UTC)
	lastError := "navigation timed out"

	mapped := mapBrowserSession(apigen.BrowserSession{
		Id:            sessionID,
		Endpoint:      "/api/v1/projects/p/browser-sessions/" + sessionID.String(),
		ExpiresAt:     expiresAt,
		Status:        apigen.BrowserSessionStatusFailed,
		DesiredStatus: apigen.BrowserSessionStatusClosed,
		ActualStatus:  apigen.BrowserSessionStatusFailed,
		LastError:     &lastError,
	})
	if mapped.ID != sessionID.String() ||
		mapped.Endpoint != "/api/v1/projects/p/browser-sessions/"+sessionID.String() ||
		mapped.ExpiresAt != expiresAt.Format(time.RFC3339Nano) ||
		mapped.Status != "failed" ||
		mapped.DesiredStatus != "closed" ||
		mapped.ActualStatus != "failed" ||
		mapped.LastError != lastError {
		t.Fatalf("mapBrowserSession() = %+v", mapped)
	}
}

func TestParseUUIDHelpers(t *testing.T) {
	valid := uuid.New()
	got, err := parseNonzeroUUID("project_id", valid.String())
	if err != nil || got != valid {
		t.Fatalf("parseNonzeroUUID(valid) = (%s, %v)", got, err)
	}
	if _, err := parseNonzeroUUID("project_id", uuid.Nil.String()); err == nil {
		t.Fatal("parseNonzeroUUID accepted nil UUID")
	}
	if _, err := parseNonzeroUUID("project_id", "invalid"); err == nil {
		t.Fatal("parseNonzeroUUID accepted invalid UUID")
	}

	optionalNil, err := parseOptionalUUID("page_id", "  ")
	if err != nil || optionalNil != nil {
		t.Fatalf("parseOptionalUUID(blank) = (%v, %v)", optionalNil, err)
	}
	optionalValid, err := parseOptionalUUID("page_id", valid.String())
	if err != nil || optionalValid == nil || *optionalValid != valid {
		t.Fatalf("parseOptionalUUID(valid) = (%v, %v)", optionalValid, err)
	}
	if _, err := parseOptionalUUID("page_id", uuid.Nil.String()); err == nil {
		t.Fatal("parseOptionalUUID accepted nil UUID")
	}
}

func TestBrowserToolsRegisteredAndValidateInputs(t *testing.T) {
	handler, err := NewHandler("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             server.URL,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"create_browser_session", "navigate_browser_session"} {
		if !names[want] {
			t.Fatalf("missing MCP tool %q in %v", want, names)
		}
	}

	for _, tc := range []struct {
		name    string
		args    map[string]any
		wantErr string
	}{
		{
			name:    "create_browser_session",
			args:    map[string]any{"project_id": "invalid"},
			wantErr: "project_id must be a nonzero UUID",
		},
		{
			name:    "navigate_browser_session",
			args:    map[string]any{"project_id": uuid.NewString(), "session_id": uuid.Nil.String(), "url": "http://example.test"},
			wantErr: "session_id must be a nonzero UUID",
		},
		{
			name:    "navigate_browser_session",
			args:    map[string]any{"project_id": uuid.NewString(), "session_id": uuid.NewString(), "url": "   "},
			wantErr: "url must not be empty",
		},
		{
			name:    "navigate_browser_session",
			args:    map[string]any{"project_id": uuid.NewString(), "session_id": uuid.NewString(), "url": "http://example.test", "page_id": "bad"},
			wantErr: "page_id must be a nonzero UUID",
		},
	} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil {
			t.Fatalf("CallTool(%s) unexpected transport error: %v", tc.name, err)
		}
		if !result.IsError {
			t.Fatalf("CallTool(%s, %v) succeeded unexpectedly", tc.name, tc.args)
		}
		if len(result.Content) == 0 {
			t.Fatalf("CallTool(%s) returned empty error content", tc.name)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok || !strings.Contains(text.Text, tc.wantErr) {
			t.Fatalf("CallTool(%s) error = %+v, want %q", tc.name, result.Content[0], tc.wantErr)
		}
	}
	_ = http.StatusOK
}
