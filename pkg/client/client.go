package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	endpoint *url.URL
	http     *http.Client
}

func New(endpoint string) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("invalid API endpoint: %q", endpoint)
	}
	return &Client{endpoint: base, http: &http.Client{Timeout: 10 * time.Second}}, nil
}

func (c *Client) Health(ctx context.Context) (string, error) {
	endpoint := c.endpoint.ResolveReference(&url.URL{Path: "/healthz"})
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("health request returned HTTP %d", response.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.Status != "ok" {
		return "", fmt.Errorf("unexpected health status: %q", result.Status)
	}
	return result.Status, nil
}
