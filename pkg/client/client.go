package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/wendaining/cloudlet/internal/apigen"
)

type Client struct {
	endpoint *url.URL
	http     *http.Client
	api      *apigen.ClientWithResponses
	apiHTTP  *http.Client
	cookie   string
}

func New(endpoint string) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") {
		return nil, fmt.Errorf("invalid API endpoint: %q", endpoint)
	}
	client := &Client{endpoint: base, http: &http.Client{Timeout: 10 * time.Second}}
	apiTransport := http.DefaultTransport.(*http.Transport).Clone()
	apiTransport.Proxy = nil
	apiHTTP := &http.Client{
		Timeout:   10 * time.Second,
		Transport: apiTransport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	api, err := apigen.NewClientWithResponses(endpoint, apigen.WithHTTPClient(apiHTTP), apigen.WithRequestEditorFn(client.addSessionCookie))
	if err != nil {
		return nil, fmt.Errorf("create management API client: %w", err)
	}
	client.api = api
	client.apiHTTP = apiHTTP
	return client, nil
}

// SetSessionCookie configures the Cookie header used for authenticated requests.
func (c *Client) SetSessionCookie(cookie string) error {
	if strings.ContainsAny(cookie, "\r\n") {
		return fmt.Errorf("invalid session cookie")
	}
	c.cookie = strings.TrimSpace(cookie)
	return nil
}

func (c *Client) addSessionCookie(_ context.Context, request *http.Request) error {
	if c.cookie != "" {
		request.Header.Set("Cookie", c.cookie)
	}
	return nil
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
