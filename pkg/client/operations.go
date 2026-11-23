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

// ListProjectOperations returns operations visible in a project accessible to the session.
func (c *Client) ListProjectOperations(ctx context.Context, projectID string) ([]apigen.Operation, error) {
	response, err := c.api.ListProjectOperationsWithResponse(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list operations for project %q: %w", projectID, err)
	}
	if response.StatusCode() != http.StatusOK {
		return nil, &APIError{StatusCode: response.StatusCode()}
	}
	if response.JSON200 == nil {
		return nil, errors.New("list project operations returned an empty response")
	}
	return *response.JSON200, nil
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

	operations, err := c.ListProjectOperations(ctx, projectUUID.String())
	if err != nil {
		return apigen.Operation{}, err
	}
	for _, operation := range operations {
		if operation.Id == operationUUID {
			return operation, nil
		}
	}
	return apigen.Operation{}, fmt.Errorf("%w: %s in project %s", ErrOperationNotFound, operationUUID, projectUUID)
}
