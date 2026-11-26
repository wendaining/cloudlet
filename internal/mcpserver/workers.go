package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	openapitypes "github.com/oapi-codegen/runtime/types"
	"github.com/wendaining/cloudlet/pkg/client"
)

type workerPublicationInput struct {
	ProjectID         string `json:"project_id" jsonschema:"ID of the project where the caller is an owner or developer"`
	Name              string `json:"name" jsonschema:"Worker name such as example-worker"`
	IdempotencyKey    string `json:"idempotency_key" jsonschema:"Stable key to reuse when retrying the same publication"`
	Module            string `json:"module" jsonschema:"Bundled JavaScript ES module source"`
	CompatibilityDate string `json:"compatibility_date" jsonschema:"workerd compatibility date in YYYY-MM-DD format"`
	Hostname          string `json:"hostname" jsonschema:"Public hostname without a port"`
}

type workerPublicationOutput struct {
	WorkerID     string `json:"worker_id"`
	DeploymentID string `json:"deployment_id"`
	OperationID  string `json:"operation_id"`
	Hostname     string `json:"hostname"`
	State        string `json:"state"`
	Outcome      string `json:"operation_outcome"`
}

func registerWorkerTools(server *mcp.Server, endpoint string) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "publish_worker", Description: "Publish a project's first Worker version using the caller's management session. Reuse the idempotency key to retry the same request.",
	}, func(ctx context.Context, request *mcp.CallToolRequest, input workerPublicationInput) (*mcp.CallToolResult, workerPublicationOutput, error) {
		api, err := requestClient(endpoint, request)
		if err != nil {
			return nil, workerPublicationOutput{}, err
		}
		projectID, err := uuid.Parse(input.ProjectID)
		if err != nil || projectID == uuid.Nil {
			return nil, workerPublicationOutput{}, errors.New("project_id must be a nonzero UUID")
		}
		date, err := time.Parse(time.DateOnly, input.CompatibilityDate)
		if err != nil {
			return nil, workerPublicationOutput{}, fmt.Errorf("compatibility_date must be a calendar date in YYYY-MM-DD format: %w", err)
		}
		deployment, err := api.PublishWorker(ctx, projectID.String(), input.Name, input.IdempotencyKey, client.WorkerPublicationRequest{
			Module: input.Module, CompatibilityDate: openapitypes.Date{Time: date}, Hostname: input.Hostname,
		})
		if deployment.Id == uuid.Nil {
			if err == nil {
				err = errors.New("management API did not return the published deployment ID")
			}
			return nil, workerPublicationOutput{}, err
		}
		result := workerPublicationOutput{
			WorkerID: deployment.WorkerId.String(), DeploymentID: deployment.Id.String(), OperationID: deployment.OperationId.String(),
			Hostname: deployment.Hostname, State: deployment.State, Outcome: deployment.OperationOutcome,
		}
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, result, nil
		}
		return nil, result, nil
	})
}
