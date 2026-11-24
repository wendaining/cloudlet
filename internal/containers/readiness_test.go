package containers

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func validReadinessProbe() ReadinessProbe {
	return ReadinessProbe{
		Port:     8080,
		Path:     "/",
		Timeout:  time.Second,
		Interval: 10 * time.Millisecond,
		Statuses: []int{http.StatusOK},
	}
}

func probeObservation(t *testing.T, address string, running bool) Observation {
	t.Helper()
	parsed, err := url.Parse("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	hostPort, err := strconv.ParseUint(parsed.Port(), 10, 16)
	if err != nil {
		t.Fatalf("parse test server port %q: %v", parsed.Port(), err)
	}
	observation := Observation{
		ID:      "0123456789ab",
		Name:    "cloudlet-test",
		Status:  "running",
		Running: true,
		Ports: []PublishedPort{{
			ContainerPort: 8080,
			Protocol:      "tcp",
			HostIP:        netip.MustParseAddr("127.0.0.1"),
			HostPort:      uint16(hostPort),
		}},
	}
	if !running {
		observation.Status = "exited"
		observation.Running = false
		observation.ExitCode = 7
	}
	return observation
}

func fixedObservation(observation Observation) func(context.Context) (Observation, error) {
	return func(context.Context) (Observation, error) {
		return observation, nil
	}
}

func TestReadinessProbeValidate(t *testing.T) {
	if err := validReadinessProbe().validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		change func(*ReadinessProbe)
	}{
		{"zero port", func(p *ReadinessProbe) { p.Port = 0 }},
		{"empty path", func(p *ReadinessProbe) { p.Path = "" }},
		{"relative path", func(p *ReadinessProbe) { p.Path = "ready" }},
		{"absolute URL", func(p *ReadinessProbe) { p.Path = "http://example.com/ready" }},
		{"protocol-relative path", func(p *ReadinessProbe) { p.Path = "//example.com/ready" }},
		{"fragment", func(p *ReadinessProbe) { p.Path = "/ready#fragment" }},
		{"space", func(p *ReadinessProbe) { p.Path = "/a b" }},
		{"NUL byte", func(p *ReadinessProbe) { p.Path = "/a\x00b" }},
		{"path too long", func(p *ReadinessProbe) { p.Path = "/" + strings.Repeat("a", maxProbePath) }},
		{"zero timeout", func(p *ReadinessProbe) { p.Timeout = 0 }},
		{"negative timeout", func(p *ReadinessProbe) { p.Timeout = -time.Second }},
		{"zero interval", func(p *ReadinessProbe) { p.Interval = 0 }},
		{"negative interval", func(p *ReadinessProbe) { p.Interval = -time.Millisecond }},
		{"no statuses", func(p *ReadinessProbe) { p.Statuses = nil }},
		{"status below range", func(p *ReadinessProbe) { p.Statuses = []int{99} }},
		{"informational status 100", func(p *ReadinessProbe) { p.Statuses = []int{100} }},
		{"switching protocols status 101", func(p *ReadinessProbe) { p.Statuses = []int{101} }},
		{"informational status 199", func(p *ReadinessProbe) { p.Statuses = []int{199} }},
		{"status above range", func(p *ReadinessProbe) { p.Statuses = []int{600} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := validReadinessProbe()
			test.change(&probe)
			if err := probe.validate(); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("validate() = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestReadinessProbeValidateAcceptsQueryAndStatusRange(t *testing.T) {
	for _, path := range []string{"/", "/ready", "/ready?probe=1", "/a/b?x=1&y=2", "/.well-known/health"} {
		probe := validReadinessProbe()
		probe.Path = path
		if err := probe.validate(); err != nil {
			t.Fatalf("validate() with path %q = %v", path, err)
		}
	}
	for _, status := range []int{200, 204, 302, 404, 503, 599} {
		probe := validReadinessProbe()
		probe.Statuses = []int{status}
		if err := probe.validate(); err != nil {
			t.Fatalf("validate() with status %d = %v", status, err)
		}
	}
}

func TestWaitReadyRejectsInformationalStatusBeforeDocker(t *testing.T) {
	// A nil Docker client proves validation happens before any container
	// observation. Informational responses are consumed by net/http, so they
	// cannot be used as readiness outcomes.
	adapter := &Adapter{}
	for _, status := range []int{100, 101, 103, 199} {
		probe := validReadinessProbe()
		probe.Statuses = []int{status}
		_, err := adapter.WaitReady(context.Background(), validOwnership(), "0123456789ab", probe)
		if err == nil || !errors.Is(err, fs.ErrInvalid) || !strings.Contains(err.Error(), "final response") {
			t.Fatalf("WaitReady() with status %d = %v, want final-response validation error", status, err)
		}
	}
}

func TestReadinessProbeAccepts(t *testing.T) {
	probe := validReadinessProbe()
	probe.Statuses = []int{http.StatusOK, http.StatusNoContent}
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		if !probe.accepts(status) {
			t.Fatalf("accepts(%d) = false, want true", status)
		}
	}
	for _, status := range []int{http.StatusCreated, http.StatusServiceUnavailable} {
		if probe.accepts(status) {
			t.Fatalf("accepts(%d) = true, want false", status)
		}
	}
}

func TestWaitReadyRetriesUntilAccepted(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.URL.RequestURI(); got != "/ready?probe=1" {
			t.Errorf("request URI = %q, want /ready?probe=1", got)
		}
		if requests.Add(1) <= 2 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	probe := validReadinessProbe()
	probe.Path = "/ready?probe=1"
	probe.Timeout = 2 * time.Second
	probe.Statuses = []int{http.StatusNoContent}
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready {
		t.Fatalf("readiness = %+v, want ready", readiness)
	}
	if readiness.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", readiness.Attempts)
	}
	if readiness.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", readiness.StatusCode)
	}
	if want := server.URL + "/ready?probe=1"; readiness.Endpoint != want {
		t.Fatalf("endpoint = %q, want %q", readiness.Endpoint, want)
	}
	if readiness.Observation.ID != "0123456789ab" || !readiness.Observation.Running {
		t.Fatalf("observation = %+v", readiness.Observation)
	}
	if readiness.Elapsed <= 0 {
		t.Fatalf("elapsed = %s, want positive", readiness.Elapsed)
	}
}

func TestWaitReadyAcceptsConfiguredStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	probe := validReadinessProbe()
	probe.Statuses = []int{http.StatusServiceUnavailable}
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.StatusCode != http.StatusServiceUnavailable || readiness.Attempts != 1 {
		t.Fatalf("readiness = %+v, want ready with status 503", readiness)
	}
}

func TestWaitReadyStopsWhenAcceptedBodyStaysOpen(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)

	probe := validReadinessProbe()
	probe.Timeout = 2 * time.Second
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.StatusCode != http.StatusOK || readiness.Attempts != 1 {
		t.Fatalf("readiness = %+v, want ready after the first status", readiness)
	}
	if elapsed > time.Second {
		t.Fatalf("elapsed = %s, want the probe to stop when the accepted status arrives", elapsed)
	}
}

func TestWaitReadyRetriesWhenRejectedBodyStaysOpen(t *testing.T) {
	release := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			writer.(http.Flusher).Flush()
			<-release
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	probe := validReadinessProbe()
	probe.Timeout = 2 * time.Second
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if !readiness.Ready || readiness.StatusCode != http.StatusOK || readiness.Attempts != 2 {
		t.Fatalf("readiness = %+v, want ready on the second attempt", readiness)
	}
	if elapsed > time.Second {
		t.Fatalf("elapsed = %s, want the rejected streaming body to be closed immediately", elapsed)
	}
}

func TestWaitReadyTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	probe := validReadinessProbe()
	probe.Timeout = 200 * time.Millisecond
	probe.Interval = 20 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want context.DeadlineExceeded", err)
	}
	if readiness.Ready {
		t.Fatalf("readiness = %+v, want not ready", readiness)
	}
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", readiness.StatusCode)
	}
	if readiness.Attempts < 2 {
		t.Fatalf("attempts = %d, want at least 2", readiness.Attempts)
	}
	if elapsed < probe.Timeout {
		t.Fatalf("elapsed = %s, want at least %s", elapsed, probe.Timeout)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want the probe deadline to stop retries", elapsed)
	}
}

func TestWaitReadyTimeoutInterruptsHungRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	probe := validReadinessProbe()
	probe.Timeout = 150 * time.Millisecond
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady and DeadlineExceeded", err)
	}
	if readiness.Ready || readiness.Attempts != 1 || readiness.StatusCode != 0 {
		t.Fatalf("readiness = %+v, want one request without a response", readiness)
	}
	if elapsed < probe.Timeout || elapsed > 5*time.Second {
		t.Fatalf("elapsed = %s, want the deadline to interrupt the request", elapsed)
	}
}

func TestWaitReadyRejectsOversizedResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Large", strings.Repeat("a", maxProbeHeaderBytes+1))
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	probe := validReadinessProbe()
	probe.Timeout = 200 * time.Millisecond
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady and DeadlineExceeded", err)
	}
	if readiness.Ready || readiness.StatusCode != 0 || readiness.Attempts == 0 {
		t.Fatalf("readiness = %+v, want failed requests without a status", readiness)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed = %s, want the probe deadline to stop retries", elapsed)
	}
}

func TestWaitReadyHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	probe := validReadinessProbe()
	probe.Timeout = 10 * time.Second
	start := time.Now()
	readiness, err := waitForHTTP(ctx, newProbeClient(), probe, fixedObservation(probeObservation(t, server.Listener.Addr().String(), true)))
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady() error = %v, want context.Canceled", err)
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady", err)
	}
	if readiness.Ready || readiness.Attempts != 1 {
		t.Fatalf("readiness = %+v, want one failed attempt", readiness)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed = %s, want cancellation to stop the probe", elapsed)
	}
}

func TestWaitReadyFailsWhenContainerStops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	running := probeObservation(t, server.Listener.Addr().String(), true)
	stopped := probeObservation(t, server.Listener.Addr().String(), false)
	var calls atomic.Int32
	observe := func(context.Context) (Observation, error) {
		if calls.Add(1) == 1 {
			return running, nil
		}
		return stopped, nil
	}
	probe := validReadinessProbe()
	probe.Timeout = 5 * time.Second
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, observe)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady", err)
	}
	if !strings.Contains(err.Error(), "container is not running") {
		t.Fatalf("WaitReady() error = %v, want not-running detail", err)
	}
	if readiness.Ready || readiness.Observation.Running || readiness.Observation.ExitCode != 7 {
		t.Fatalf("readiness = %+v, want stopped observation", readiness)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("elapsed = %s, want the probe to stop when the container exits", elapsed)
	}
}

func TestNewProbeClient(t *testing.T) {
	client := newProbeClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("probe transport must not use an environment proxy")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("probe transport must not keep idle connections to stopped containers")
	}
	if transport.MaxResponseHeaderBytes != maxProbeHeaderBytes {
		t.Fatalf("MaxResponseHeaderBytes = %d, want %d", transport.MaxResponseHeaderBytes, maxProbeHeaderBytes)
	}
	if client.CheckRedirect == nil {
		t.Fatal("probe client must not follow redirects")
	}
}

func TestWaitReadyRetriesMissingPortUntilDeadline(t *testing.T) {
	observation := Observation{
		ID:      "0123456789ab",
		Status:  "running",
		Running: true,
	}
	probe := validReadinessProbe()
	probe.Timeout = 100 * time.Millisecond
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(observation))
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady and DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), "is not published") {
		t.Fatalf("WaitReady() error = %v, want missing-port detail", err)
	}
	if readiness.Attempts != 0 || readiness.StatusCode != 0 {
		t.Fatalf("readiness = %+v, want no HTTP attempts", readiness)
	}
	if elapsed < probe.Timeout {
		t.Fatalf("elapsed = %s, want at least %s", elapsed, probe.Timeout)
	}
}

func TestWaitReadyPreservesLastStatusWhenPortDisappears(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	withPort := probeObservation(t, server.Listener.Addr().String(), true)
	withoutPort := Observation{
		ID:      withPort.ID,
		Status:  "running",
		Running: true,
	}
	var calls atomic.Int32
	observe := func(context.Context) (Observation, error) {
		if calls.Add(1) == 1 {
			return withPort, nil
		}
		return withoutPort, nil
	}
	probe := validReadinessProbe()
	probe.Timeout = 100 * time.Millisecond
	probe.Interval = 10 * time.Millisecond
	start := time.Now()
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, observe)
	elapsed := time.Since(start)
	if !errors.Is(err, ErrNotReady) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady and DeadlineExceeded", err)
	}
	if readiness.Attempts != 1 {
		t.Fatalf("attempts = %d, want one HTTP attempt", readiness.Attempts)
	}
	if readiness.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the last observed 503", readiness.StatusCode)
	}
	if !strings.Contains(err.Error(), "last status 503") {
		t.Fatalf("WaitReady() error = %v, want last status 503", err)
	}
	if !strings.Contains(err.Error(), "is not published") {
		t.Fatalf("WaitReady() error = %v, want missing-port detail", err)
	}
	if elapsed < probe.Timeout {
		t.Fatalf("elapsed = %s, want at least %s", elapsed, probe.Timeout)
	}
}

func TestWaitReadyPropagatesObservationError(t *testing.T) {
	observe := func(context.Context) (Observation, error) {
		return Observation{}, ErrNotOwned
	}
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), validReadinessProbe(), observe)
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("WaitReady() error = %v, want ErrNotOwned", err)
	}
	if errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady() error = %v, must not report readiness failure for ownership errors", err)
	}
	if readiness.Ready || readiness.Attempts != 0 {
		t.Fatalf("readiness = %+v, want no attempts", readiness)
	}
}

func TestWaitReadyDoesNotFollowRedirects(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		writer.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	probe := validReadinessProbe()
	probe.Timeout = 150 * time.Millisecond
	probe.Interval = 10 * time.Millisecond
	readiness, err := waitForHTTP(context.Background(), newProbeClient(), probe, fixedObservation(probeObservation(t, redirect.Listener.Addr().String(), true)))
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("WaitReady() error = %v, want ErrNotReady", err)
	}
	if readiness.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", readiness.StatusCode)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests, want 0", targetRequests.Load())
	}
}

func TestWaitReadyRejectsInvalidProbeBeforeDocker(t *testing.T) {
	adapter := &Adapter{}
	_, err := adapter.WaitReady(context.Background(), validOwnership(), "0123456789ab", ReadinessProbe{})
	if err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("WaitReady() error = %v, want fs.ErrInvalid", err)
	}
}

func TestWaitReadyRequiresHTTPClient(t *testing.T) {
	adapter := &Adapter{}
	_, err := adapter.WaitReady(context.Background(), validOwnership(), "0123456789ab", validReadinessProbe())
	if err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("WaitReady() error = %v, want fs.ErrInvalid", err)
	}
}

func TestWaitReadyRejectsInvalidOwnershipBeforeDocker(t *testing.T) {
	adapter := &Adapter{}
	ownership := validOwnership()
	ownership.Project = ""
	_, err := adapter.WaitReady(context.Background(), ownership, "0123456789ab", validReadinessProbe())
	if err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("WaitReady() error = %v, want fs.ErrInvalid", err)
	}
}

func TestWaitReadyRejectsInvalidContainerIDBeforeDocker(t *testing.T) {
	adapter := &Adapter{}
	_, err := adapter.WaitReady(context.Background(), validOwnership(), "short", validReadinessProbe())
	if err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("WaitReady() error = %v, want fs.ErrInvalid", err)
	}
}
