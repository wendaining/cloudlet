package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

var ErrOperationNotFound = errors.New("operation not found")

// APIError describes a non-success response from the management API.
type APIError struct {
	StatusCode int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("management API returned HTTP %d", e.StatusCode)
}

// ProjectOperation returns an operation visible in the specified project.
// It selects from the authenticated project-scoped list so the API enforces
// membership without requiring a new endpoint.
func (c *Client) ProjectOperation(ctx context.Context, projectID, operationID string) (apigen.Operation, error) {
	projectUUID, err := uuid.Parse(projectID)
	if err != nil {
		return apigen.Operation{}, fmt.Errorf("invalid project ID %q: %w", projectID, err)
	}
	operationUUID, err := uuid.Parse(operationID)
	if err != nil {
		return apigen.Operation{}, fmt.Errorf("invalid operation ID %q: %w", operationID, err)
	}

	response, err := c.api.ListProjectOperationsWithResponse(ctx, projectUUID.String())
	if err != nil {
		return apigen.Operation{}, fmt.Errorf("list project operations: %w", err)
	}
	if response.StatusCode() != http.StatusOK {
		return apigen.Operation{}, &APIError{StatusCode: response.StatusCode()}
	}
	if response.JSON200 == nil {
		return apigen.Operation{}, fmt.Errorf("list project operations returned an empty response")
	}
	for _, operation := range *response.JSON200 {
		foundUUID, err := uuid.Parse(operation.Id.String())
		if err != nil {
			return apigen.Operation{}, fmt.Errorf("API returned invalid operation ID %q: %w", operation.Id, err)
		}
		if foundUUID == operationUUID {
			return operation, nil
		}
	}
	return apigen.Operation{}, fmt.Errorf("%w: %s in project %s", ErrOperationNotFound, operationUUID, projectUUID)
}
