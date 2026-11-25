package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/pkg/client"
)

type projectInput struct {
	ProjectID string `json:"project_id" jsonschema:"ID of a project visible to the caller"`
}

type operationInput struct {
	ProjectID   string `json:"project_id" jsonschema:"ID of a project visible to the caller"`
	OperationID string `json:"operation_id" jsonschema:"ID of an operation in the project"`
}

type projectsOutput struct {
	Projects []projectOutput `json:"projects"`
}

type projectOutput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type operationsOutput struct {
	Operations []operationOutput `json:"operations"`
}

type operationOutput struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Phase     string `json:"phase"`
	Outcome   string `json:"outcome"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func NewHandler(apiEndpoint string) (http.Handler, error) {
	if _, err := client.New(apiEndpoint); err != nil {
		return nil, fmt.Errorf("configure MCP management API client: %w", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "cloudlet", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "list_projects", Description: "List projects visible to the authenticated caller."},
		func(ctx context.Context, request *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, projectsOutput, error) {
			api, err := requestClient(apiEndpoint, request)
			if err != nil {
				return nil, projectsOutput{}, err
			}
			projects, err := api.ListProjects(ctx)
			if err != nil {
				return nil, projectsOutput{}, err
			}
			return nil, projectsOutput{Projects: mapProjects(projects)}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "get_project", Description: "Get a project visible to the authenticated caller."},
		func(ctx context.Context, request *mcp.CallToolRequest, input projectInput) (*mcp.CallToolResult, projectOutput, error) {
			api, err := requestClient(apiEndpoint, request)
			if err != nil {
				return nil, projectOutput{}, err
			}
			project, err := api.GetProject(ctx, input.ProjectID)
			if err != nil {
				return nil, projectOutput{}, err
			}
			return nil, mapProject(project), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "list_project_operations", Description: "List operations in a project accessible to the authenticated caller."},
		func(ctx context.Context, request *mcp.CallToolRequest, input projectInput) (*mcp.CallToolResult, operationsOutput, error) {
			api, err := requestClient(apiEndpoint, request)
			if err != nil {
				return nil, operationsOutput{}, err
			}
			operations, err := api.ListProjectOperations(ctx, input.ProjectID)
			if err != nil {
				return nil, operationsOutput{}, err
			}
			return nil, operationsOutput{Operations: mapOperations(operations)}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "get_operation", Description: "Get an operation in a project accessible to the authenticated caller."},
		func(ctx context.Context, request *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, operationOutput, error) {
			api, err := requestClient(apiEndpoint, request)
			if err != nil {
				return nil, operationOutput{}, err
			}
			operation, err := api.ProjectOperation(ctx, input.ProjectID, input.OperationID)
			if err != nil {
				return nil, operationOutput{}, err
			}
			return nil, mapOperation(operation), nil
		})
	registerWorkerTools(server, apiEndpoint)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true,
	})
	return handler, nil
}

func requestClient(endpoint string, request *mcp.CallToolRequest) (*client.Client, error) {
	api, err := client.New(endpoint)
	if err != nil {
		return nil, fmt.Errorf("create management API client: %w", err)
	}
	if request.Extra == nil {
		return nil, errors.New("missing MCP HTTP request metadata")
	}
	if err := api.SetSessionCookie(request.Extra.Header.Get("Cookie")); err != nil {
		return nil, fmt.Errorf("forward caller session: %w", err)
	}
	return api, nil
}

func mapProjects(projects []apigen.Project) []projectOutput {
	result := make([]projectOutput, 0, len(projects))
	for _, project := range projects {
		result = append(result, mapProject(project))
	}
	return result
}

func mapProject(project apigen.Project) projectOutput {
	return projectOutput{ID: project.Id.String(), Name: project.Name}
}

func mapOperations(operations []apigen.Operation) []operationOutput {
	result := make([]operationOutput, 0, len(operations))
	for _, operation := range operations {
		result = append(result, mapOperation(operation))
	}
	return result
}

func mapOperation(operation apigen.Operation) operationOutput {
	return operationOutput{
		ID: operation.Id.String(), Action: operation.Action, Phase: operation.Phase, Outcome: operation.Outcome,
		CreatedAt: operation.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: operation.UpdatedAt.Format(time.RFC3339Nano),
	}
}
