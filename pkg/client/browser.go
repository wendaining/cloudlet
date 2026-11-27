package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func (c *Client) browserAPI() (*apigen.ClientWithResponses, error) {
	transport := *c.apiHTTP
	transport.Timeout = 95 * time.Second
	return apigen.NewClientWithResponses(c.endpoint.String(), apigen.WithHTTPClient(&transport), apigen.WithRequestEditorFn(c.addSessionCookie))
}

// CreateBrowserSession creates a ready Browser Run session in the specified project.
func (c *Client) CreateBrowserSession(ctx context.Context, projectID string) (apigen.BrowserSession, error) {
	projectUUID, err := parseNonzeroUUID("project ID", projectID)
	if err != nil {
		return apigen.BrowserSession{}, err
	}
	api, err := c.browserAPI()
	if err != nil {
		return apigen.BrowserSession{}, fmt.Errorf("create browser API client: %w", err)
	}
	response, err := api.CreateBrowserSessionWithResponse(ctx, projectUUID.String())
	if err != nil {
		return apigen.BrowserSession{}, fmt.Errorf("create browser session in project %s: %w", projectUUID, err)
	}
	if response.JSON201 != nil {
		return *response.JSON201, nil
	}
	return apigen.BrowserSession{}, responseError("create browser session", response.StatusCode(), response.JSONDefault)
}

// NavigateBrowserSession navigates an active Browser Run session to targetURL.
func (c *Client) NavigateBrowserSession(ctx context.Context, projectID string, sessionID uuid.UUID, targetURL string, pageID *uuid.UUID) (apigen.BrowserNavigation, error) {
	projectUUID, err := parseNonzeroUUID("project ID", projectID)
	if err != nil {
		return apigen.BrowserNavigation{}, err
	}
	if sessionID == uuid.Nil {
		return apigen.BrowserNavigation{}, errors.New("session ID must be a nonzero UUID")
	}
	if strings.TrimSpace(targetURL) == "" {
		return apigen.BrowserNavigation{}, errors.New("navigation URL must not be empty")
	}
	if pageID != nil && *pageID == uuid.Nil {
		return apigen.BrowserNavigation{}, errors.New("page ID must be a nonzero UUID")
	}
	api, err := c.browserAPI()
	if err != nil {
		return apigen.BrowserNavigation{}, fmt.Errorf("create browser API client: %w", err)
	}
	response, err := api.NavigateBrowserSessionWithResponse(ctx, projectUUID.String(), sessionID, apigen.NavigateBrowserSessionJSONRequestBody{
		Url:    targetURL,
		PageId: pageID,
	})
	if err != nil {
		return apigen.BrowserNavigation{}, fmt.Errorf("navigate browser session %s in project %s: %w", sessionID, projectUUID, err)
	}
	if response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return apigen.BrowserNavigation{}, responseError("navigate browser session", response.StatusCode(), response.JSONDefault)
}

// CloseBrowserSession closes a Browser Run session in the specified project.
func (c *Client) CloseBrowserSession(ctx context.Context, projectID string, sessionID uuid.UUID) (apigen.BrowserSession, error) {
	projectUUID, err := parseNonzeroUUID("project ID", projectID)
	if err != nil {
		return apigen.BrowserSession{}, err
	}
	if sessionID == uuid.Nil {
		return apigen.BrowserSession{}, errors.New("session ID must be a nonzero UUID")
	}
	api, err := c.browserAPI()
	if err != nil {
		return apigen.BrowserSession{}, fmt.Errorf("create browser API client: %w", err)
	}
	response, err := api.CloseBrowserSessionWithResponse(ctx, projectUUID.String(), sessionID)
	if err != nil {
		return apigen.BrowserSession{}, fmt.Errorf("close browser session %s in project %s: %w", sessionID, projectUUID, err)
	}
	if response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return apigen.BrowserSession{}, responseError("close browser session", response.StatusCode(), response.JSON403, response.JSON404, response.JSONDefault)
}

func parseNonzeroUUID(label, raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%s must be a nonzero UUID", label)
	}
	return id, nil
}
