package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendaining/cloudlet/internal/apigen"
)

type browserSessionInput struct {
	ProjectID string `json:"project_id" jsonschema:"ID of the project where the caller is an owner or developer"`
}

type browserSessionOutput struct {
	ID            string `json:"id"`
	Endpoint      string `json:"endpoint"`
	ExpiresAt     string `json:"expires_at"`
	Status        string `json:"status"`
	DesiredStatus string `json:"desired_status"`
	ActualStatus  string `json:"actual_status"`
	LastError     string `json:"last_error,omitempty"`
}

type navigateBrowserSessionInput struct {
	ProjectID string `json:"project_id" jsonschema:"ID of the project where the caller is an owner or developer"`
	SessionID string `json:"session_id" jsonschema:"ID of an active Browser Run session in the project"`
	URL       string `json:"url" jsonschema:"Target URL to navigate to"`
	PageID    string `json:"page_id,omitempty" jsonschema:"Optional page ID; omit to navigate the default page"`
}

type browserNavigationOutput struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}

func registerBrowserTools(server *mcp.Server, endpoint string) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "create_browser_session",
		Description: "Create a Browser Run session in an authorized project using the caller's management session.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input browserSessionInput) (*mcp.CallToolResult, browserSessionOutput, error) {
		api, err := requestClient(endpoint, request)
		if err != nil {
			return nil, browserSessionOutput{}, err
		}
		projectID, err := parseNonzeroUUID("project_id", input.ProjectID)
		if err != nil {
			return nil, browserSessionOutput{}, err
		}
		session, err := api.CreateBrowserSession(ctx, projectID.String())
		if err != nil {
			return nil, browserSessionOutput{}, err
		}
		return nil, mapBrowserSession(session), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "navigate_browser_session",
		Description: "Navigate an authorized Browser Run session to a target URL and return the resulting URL and page title.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input navigateBrowserSessionInput) (*mcp.CallToolResult, browserNavigationOutput, error) {
		api, err := requestClient(endpoint, request)
		if err != nil {
			return nil, browserNavigationOutput{}, err
		}
		projectID, err := parseNonzeroUUID("project_id", input.ProjectID)
		if err != nil {
			return nil, browserNavigationOutput{}, err
		}
		sessionID, err := parseNonzeroUUID("session_id", input.SessionID)
		if err != nil {
			return nil, browserNavigationOutput{}, err
		}
		if strings.TrimSpace(input.URL) == "" {
			return nil, browserNavigationOutput{}, errors.New("url must not be empty")
		}
		pageID, err := parseOptionalUUID("page_id", input.PageID)
		if err != nil {
			return nil, browserNavigationOutput{}, err
		}
		navigation, err := api.NavigateBrowserSession(ctx, projectID.String(), sessionID, input.URL, pageID)
		if err != nil {
			return nil, browserNavigationOutput{}, err
		}
		return nil, browserNavigationOutput{URL: navigation.Url, Title: navigation.Title}, nil
	})
}

func mapBrowserSession(session apigen.BrowserSession) browserSessionOutput {
	output := browserSessionOutput{
		ID:            session.Id.String(),
		Endpoint:      session.Endpoint,
		ExpiresAt:     session.ExpiresAt.Format(time.RFC3339Nano),
		Status:        string(session.Status),
		DesiredStatus: string(session.DesiredStatus),
		ActualStatus:  string(session.ActualStatus),
	}
	if session.LastError != nil {
		output.LastError = *session.LastError
	}
	return output
}

func parseNonzeroUUID(field, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%s must be a nonzero UUID", field)
	}
	return id, nil
}

func parseOptionalUUID(field, raw string) (*uuid.UUID, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	id, err := parseNonzeroUUID(field, raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
