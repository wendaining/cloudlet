package containers

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	// maxProbePath bounds the request path so a malformed configuration cannot
	// build an unbounded request URL.
	maxProbePath = 2048
	// maxProbeHeaderBytes bounds response headers from a container so a
	// misbehaving service cannot make the probe allocate an unbounded buffer.
	maxProbeHeaderBytes = 64 << 10
)

var (
	// ErrNotReady reports that a container did not answer an accepted HTTP
	// status before the readiness deadline, stopped while the probe was
	// running, or did not publish the requested port.
	ErrNotReady = errors.New("container not ready")
)

// ReadinessProbe describes a deadline-bounded HTTP readiness check against a
// container's actual published loopback endpoint. Port is the container TCP
// port whose Docker-reported host binding is requested, Path is the origin-form
// request path, Timeout is the overall probe deadline, Interval is the pause
// between attempts, and Statuses lists the accepted final HTTP status codes
// between 200 and 599. Informational responses are rejected because net/http
// consumes 1xx responses other than 101 before returning the final response,
// and 101 is a protocol switch rather than a readiness result.
type ReadinessProbe struct {
	Port     uint16
	Path     string
	Timeout  time.Duration
	Interval time.Duration
	Statuses []int
}

// Readiness is the observation returned by WaitReady. Ready reports whether an
// accepted status was observed. Endpoint is the actual probed URL, Attempts is
// the number of HTTP requests, StatusCode is the last HTTP status code observed
// (zero when no response was ever received), Elapsed is the total probe time,
// and Observation is the last inspected container state.
type Readiness struct {
	Ready       bool
	Endpoint    string
	Attempts    int
	StatusCode  int
	Elapsed     time.Duration
	Observation Observation
}

// WaitReady probes the actual published loopback endpoint of a container that
// belongs to the requested ownership until an accepted HTTP status is observed
// or the probe deadline expires. The container must already be started.
//
// A stopped container, a container that exits during the probe, and an expired
// deadline return ErrNotReady with the last observation. Ownership and lookup
// errors are returned unchanged. Invalid probe configuration returns
// fs.ErrInvalid before Docker is contacted.
func (a *Adapter) WaitReady(ctx context.Context, ownership Ownership, id string, probe ReadinessProbe) (Readiness, error) {
	if err := probe.validate(); err != nil {
		return Readiness{}, err
	}
	if err := ownership.validate(); err != nil {
		return Readiness{}, err
	}
	if err := validateContainerID(id); err != nil {
		return Readiness{}, err
	}
	if a.httpClient == nil {
		return Readiness{}, fmt.Errorf("wait for container %s readiness: HTTP client is not configured: %w", id, fs.ErrInvalid)
	}
	return waitForHTTP(ctx, a.httpClient, probe, func(ctx context.Context) (Observation, error) {
		return a.Observe(ctx, ownership, id)
	})
}

// newProbeClient returns the HTTP client used for readiness probes. Probes
// target loopback endpoints, so environment proxies are disabled and redirects
// are returned as observed status codes instead of being followed. Keep-alives
// are disabled because probes are one-shot and a stopped or replaced container
// must not leave a stale idle connection in a long-lived adapter.
func newProbeClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.MaxResponseHeaderBytes = maxProbeHeaderBytes
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// waitForHTTP runs the readiness loop. observe returns the current container
// state and actual port bindings; it is called before every HTTP attempt so the
// probe follows Docker's reported endpoint and notices a container that stops.
func waitForHTTP(ctx context.Context, client *http.Client, probe ReadinessProbe, observe func(context.Context) (Observation, error)) (Readiness, error) {
	ctx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()

	start := time.Now()
	result := Readiness{}
	lastStatus := 0
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			result.Elapsed = time.Since(start)
			return result, readinessError(result, lastStatus, lastErr, err)
		}
		observation, err := observe(ctx)
		if err != nil {
			result.Elapsed = time.Since(start)
			if ctx.Err() != nil {
				lastErr = err
				return result, readinessError(result, lastStatus, lastErr, ctx.Err())
			}
			return result, fmt.Errorf("wait for container readiness: %w", err)
		}
		result.Observation = observation
		if !observation.Running {
			result.Elapsed = time.Since(start)
			return result, fmt.Errorf("wait for container %s readiness: container is not running (status %s, exit code %d): %w",
				observation.ID, observation.Status, observation.ExitCode, ErrNotReady)
		}
		port, found := observation.publishedPort(probe.Port)
		if !found {
			// Docker can report the port binding slightly after start, so a
			// missing binding is retried until the deadline. Keep the last HTTP
			// status observed before the binding disappeared; it remains part
			// of the failure observation.
			result.Endpoint = ""
			lastErr = fmt.Errorf("container port %d/tcp is not published", probe.Port)
			if !sleepUntilNextAttempt(ctx, probe.Interval) {
				result.Elapsed = time.Since(start)
				return result, readinessError(result, lastStatus, lastErr, ctx.Err())
			}
			continue
		}
		result.Endpoint = "http://" + port.HostAddress() + probe.Path
		result.Attempts++
		status, err := probeOnce(ctx, client, result.Endpoint)
		if status != 0 {
			result.StatusCode = status
			lastStatus = status
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			if probe.accepts(status) {
				result.Ready = true
				result.Elapsed = time.Since(start)
				return result, nil
			}
		}
		if !sleepUntilNextAttempt(ctx, probe.Interval) {
			result.Elapsed = time.Since(start)
			return result, readinessError(result, lastStatus, lastErr, ctx.Err())
		}
	}
}

// probeOnce sends one HTTP GET request and returns the response status code.
// Readiness is decided solely by the status code, so the response body is
// closed without being read; a streaming body must not delay the probe.
func probeOnce(ctx context.Context, client *http.Client, endpoint string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("build readiness request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

// sleepUntilNextAttempt waits for the configured interval. It returns false
// when the probe context ends first.
func sleepUntilNextAttempt(ctx context.Context, interval time.Duration) bool {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readinessError(result Readiness, lastStatus int, lastErr error, cause error) error {
	detail := "no HTTP response"
	switch {
	case lastStatus != 0 && lastErr != nil:
		detail = fmt.Sprintf("last status %d, last error %v", lastStatus, lastErr)
	case lastStatus != 0:
		detail = fmt.Sprintf("last status %d", lastStatus)
	case lastErr != nil:
		detail = lastErr.Error()
	}
	target := result.Endpoint
	if target == "" {
		target = "the published endpoint"
	}
	container := result.Observation.ID
	if container == "" {
		container = "unknown"
	}
	return fmt.Errorf("wait for container %s readiness at %s: %s after %d attempts in %s: %w: %w",
		container, target, detail, result.Attempts, result.Elapsed.Round(time.Millisecond), ErrNotReady, cause)
}

func (p ReadinessProbe) validate() error {
	if p.Port == 0 {
		return fmt.Errorf("validate readiness probe: port must be between 1 and 65535: %w", fs.ErrInvalid)
	}
	if p.Path == "" {
		return fmt.Errorf("validate readiness probe: path is required: %w", fs.ErrInvalid)
	}
	if len(p.Path) > maxProbePath {
		return fmt.Errorf("validate readiness probe: path exceeds %d bytes: %w", maxProbePath, fs.ErrInvalid)
	}
	if !strings.HasPrefix(p.Path, "/") {
		return fmt.Errorf("validate readiness probe: path %q must be an absolute request path: %w", p.Path, fs.ErrInvalid)
	}
	if strings.HasPrefix(p.Path, "//") {
		return fmt.Errorf("validate readiness probe: path %q must not start with //: %w", p.Path, fs.ErrInvalid)
	}
	if strings.ContainsRune(p.Path, '#') {
		return fmt.Errorf("validate readiness probe: path %q must not contain a fragment: %w", p.Path, fs.ErrInvalid)
	}
	if strings.IndexFunc(p.Path, func(char rune) bool {
		return unicode.IsSpace(char) || unicode.IsControl(char)
	}) >= 0 {
		return fmt.Errorf("validate readiness probe: path %q contains whitespace or a control character: %w", p.Path, fs.ErrInvalid)
	}
	parsed, err := url.ParseRequestURI(p.Path)
	if err != nil {
		return fmt.Errorf("validate readiness probe: parse path %q: %w: %w", p.Path, fs.ErrInvalid, err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.Fragment != "" {
		return fmt.Errorf("validate readiness probe: path %q must not contain a scheme, host, or fragment: %w", p.Path, fs.ErrInvalid)
	}
	if p.Timeout <= 0 {
		return fmt.Errorf("validate readiness probe: timeout must be positive: %w", fs.ErrInvalid)
	}
	if p.Interval <= 0 {
		return fmt.Errorf("validate readiness probe: interval must be positive: %w", fs.ErrInvalid)
	}
	if len(p.Statuses) == 0 {
		return fmt.Errorf("validate readiness probe: at least one accepted status is required: %w", fs.ErrInvalid)
	}
	for _, status := range p.Statuses {
		if status < 200 || status > 599 {
			return fmt.Errorf("validate readiness probe: status %d must be a final response between 200 and 599: %w", status, fs.ErrInvalid)
		}
	}
	return nil
}

func (p ReadinessProbe) accepts(status int) bool {
	return slices.Contains(p.Statuses, status)
}

func (o Observation) publishedPort(port uint16) (PublishedPort, bool) {
	for _, published := range o.Ports {
		if published.ContainerPort == port && published.Protocol == "tcp" {
			return published, true
		}
	}
	return PublishedPort{}, false
}
