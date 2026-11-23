package client

import (
	"context"
	"fmt"

	"github.com/wendaining/cloudlet/internal/apigen"
)

// ListProjects returns projects visible to the authenticated session.
func (c *Client) ListProjects(ctx context.Context) ([]apigen.Project, error) {
	response, err := c.api.ListProjectsWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	if response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return nil, responseError("list projects", response.StatusCode(), response.JSON401, response.JSONDefault)
}

// GetProject returns a project visible to the authenticated session.
func (c *Client) GetProject(ctx context.Context, id string) (apigen.Project, error) {
	response, err := c.api.GetProjectWithResponse(ctx, id)
	if err != nil {
		return apigen.Project{}, fmt.Errorf("get project %q: %w", id, err)
	}
	if response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return apigen.Project{}, responseError(fmt.Sprintf("get project %q", id), response.StatusCode(), response.JSON401, response.JSONDefault)
}

func responseError(operation string, status int, errors ...*apigen.Error) error {
	for _, apiError := range errors {
		if apiError != nil && apiError.Message != "" {
			return fmt.Errorf("%s: %s (HTTP %d)", operation, apiError.Message, status)
		}
	}
	return fmt.Errorf("%s returned HTTP %d", operation, status)
}
