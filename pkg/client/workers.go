package client

import (
	"context"
	"fmt"
	"time"

	"github.com/wendaining/cloudlet/internal/apigen"
)

type WorkerPublicationRequest = apigen.DeployWorkerRequest
type WorkerDeployment = apigen.WorkerDeployment

// PublishWorker uses the shared API with time for publication and failure cleanup.
func (c *Client) PublishWorker(ctx context.Context, projectID, name, key string, request WorkerPublicationRequest) (WorkerDeployment, error) {
	transport := *c.apiHTTP
	transport.Timeout = 95 * time.Second
	api, err := apigen.NewClientWithResponses(c.endpoint.String(), apigen.WithHTTPClient(&transport), apigen.WithRequestEditorFn(c.addSessionCookie))
	if err != nil {
		return WorkerDeployment{}, fmt.Errorf("create Worker publication client: %w", err)
	}
	response, err := api.DeployWorkerWithResponse(ctx, projectID, name, &apigen.DeployWorkerParams{IdempotencyKey: key}, request)
	if err != nil {
		return WorkerDeployment{}, fmt.Errorf("publish Worker %q in project %s: %w", name, projectID, err)
	}
	if response.JSON201 != nil {
		return *response.JSON201, nil
	}
	if response.JSON200 != nil {
		if response.JSON200.OperationOutcome == "failed" {
			return *response.JSON200, fmt.Errorf("publish Worker %q: %s (operation %s)", name, response.JSON200.Failure, response.JSON200.OperationId)
		}
		return *response.JSON200, nil
	}
	for _, failure := range []*apigen.WorkerDeployment{response.JSON422, response.JSON429} {
		if failure != nil {
			return *failure, fmt.Errorf("publish Worker %q: %s (HTTP %d, operation %s)", name, failure.Failure, response.StatusCode(), failure.OperationId)
		}
	}
	return WorkerDeployment{}, responseError("publish Worker", response.StatusCode(), response.JSON400, response.JSON401, response.JSON403, response.JSON404, response.JSON409, response.JSON500, response.JSON503, response.JSONDefault)
}

func (c *Client) GetWorkerDeployment(ctx context.Context, projectID, deploymentID string) (WorkerDeployment, error) {
	response, err := c.api.GetWorkerDeploymentWithResponse(ctx, projectID, deploymentID)
	if err != nil {
		return WorkerDeployment{}, fmt.Errorf("get Worker deployment %q: %w", deploymentID, err)
	}
	if response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return WorkerDeployment{}, responseError("get Worker deployment", response.StatusCode(), response.JSON401, response.JSON404, response.JSON503, response.JSONDefault)
}
