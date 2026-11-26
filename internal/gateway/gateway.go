package gateway

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
)

type pathRoute struct {
	segments []string
	handler  http.Handler
	target   string
}

type routeSnapshot map[string][]pathRoute

type proxyEndpoint struct {
	host   string
	target string
}

type proxyRequests struct {
	count int
	done  chan struct{}
}

// Handler serves requests using the current immutable route snapshot.
type Handler struct {
	staticRoutes []config.Route
	transport    *http.Transport
	logger       *slog.Logger
	mu           sync.Mutex
	snapshot     routeSnapshot
	inflight     map[proxyEndpoint]*proxyRequests
}

func NewHandler(cfg config.Config, logger *slog.Logger) (*Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	h := &Handler{staticRoutes: slices.Clone(cfg.Routes), transport: transport, logger: logger, inflight: make(map[proxyEndpoint]*proxyRequests)}
	if err := h.ReplaceRoutes(nil); err != nil {
		return nil, err
	}
	return h, nil
}

// ReplaceRoutes atomically replaces all dynamic routes and preserves startup routes.
// A dynamic route cannot use a host reserved by startup configuration.
func (h *Handler) ReplaceRoutes(routes []config.Route) error {
	return h.ReplaceRoutesWithSites(routes, nil)
}

// SiteRoute serves one published version from the root of its host.
type SiteRoute struct {
	Host    string
	Handler http.Handler
}

// ReplaceRoutesWithSites applies proxy routes and in-process sites together.
func (h *Handler) ReplaceRoutesWithSites(routes []config.Route, sites []SiteRoute) error {
	combined := slices.Concat(h.staticRoutes, routes)
	if err := config.ValidateRoutes(combined); err != nil {
		return err
	}
	staticHosts := make(map[string]bool, len(h.staticRoutes))
	for _, route := range h.staticRoutes {
		staticHosts[CanonicalHost(route.Host)] = true
	}
	for _, route := range routes {
		if staticHosts[CanonicalHost(route.Host)] {
			return fmt.Errorf("route host %q is reserved by startup configuration", route.Host)
		}
	}
	snapshot := make(routeSnapshot, len(combined)+len(sites))
	for _, route := range combined {
		target, err := url.Parse(route.Target)
		if err != nil {
			return err
		}
		proxy := &httputil.ReverseProxy{
			Transport: h.transport,
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(target)
				request.SetXForwarded()
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				h.logger.Error("proxy request failed", "host", route.Host, "error", err)
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			},
		}
		var segments []string
		if route.PathPrefix != "" && route.PathPrefix != "/" {
			segments = strings.Split(route.PathPrefix[1:], "/")
		}
		host := CanonicalHost(route.Host)
		snapshot[host] = append(snapshot[host], pathRoute{segments: segments, handler: proxy, target: route.Target})
	}
	for _, site := range sites {
		if err := config.ValidateRouteHost(site.Host); err != nil {
			return err
		}
		host := CanonicalHost(site.Host)
		if site.Handler == nil || staticHosts[host] {
			return fmt.Errorf("invalid static site route for host %q", site.Host)
		}
		for _, route := range snapshot[host] {
			if len(route.segments) == 0 {
				return fmt.Errorf("duplicate route for host %q and path prefix /", site.Host)
			}
		}
		snapshot[host] = append(snapshot[host], pathRoute{handler: site.Handler})
	}
	for _, routes := range snapshot {
		slices.SortFunc(routes, func(a, b pathRoute) int {
			return cmp.Compare(len(b.segments), len(a.segments))
		})
	}
	h.mu.Lock()
	h.snapshot = snapshot
	h.mu.Unlock()
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := CanonicalHost(r.Host)
	h.mu.Lock()
	route, status := selectRoute(h.snapshot[host], r.URL)
	endpoint := proxyEndpoint{host: host, target: route.target}
	if route.target != "" {
		requests := h.inflight[endpoint]
		if requests == nil {
			requests = &proxyRequests{done: make(chan struct{})}
			h.inflight[endpoint] = requests
		}
		requests.count++
	}
	h.mu.Unlock()

	if route.target != "" {
		defer h.finishProxyRequest(endpoint)
	}
	switch status {
	case http.StatusBadRequest:
		http.Error(w, "invalid request path", status)
	case http.StatusNotFound:
		http.NotFound(w, r)
	default:
		route.handler.ServeHTTP(w, r)
	}
}

func selectRoute(routes []pathRoute, requestURL *url.URL) (pathRoute, int) {
	if len(routes) == 0 {
		return pathRoute{}, http.StatusNotFound
	}
	if len(routes[0].segments) == 0 {
		return routes[0], http.StatusOK
	}
	// Split before decoding so an escaped slash stays in its original segment.
	segments := strings.Split(strings.TrimPrefix(requestURL.EscapedPath(), "/"), "/")
	for i, segment := range segments {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." {
			return pathRoute{}, http.StatusBadRequest
		}
		segments[i] = decoded
	}
	for _, route := range routes {
		if n := len(route.segments); n <= len(segments) && slices.Equal(route.segments, segments[:n]) {
			return route, http.StatusOK
		}
	}
	return pathRoute{}, http.StatusNotFound
}

func (h *Handler) finishProxyRequest(endpoint proxyEndpoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	requests := h.inflight[endpoint]
	requests.count--
	if requests.count == 0 {
		close(requests.done)
		delete(h.inflight, endpoint)
	}
}

// DrainProxy waits for requests that selected the endpoint before its removal.
func (h *Handler) DrainProxy(ctx context.Context, host, target string) error {
	host = CanonicalHost(host)
	h.mu.Lock()
	for _, route := range h.snapshot[host] {
		if route.target == target {
			h.mu.Unlock()
			return fmt.Errorf("cannot drain Gateway upstream %q for %q while it is routed", target, host)
		}
	}
	requests := h.inflight[proxyEndpoint{host: host, target: target}]
	h.mu.Unlock()
	if requests == nil {
		return nil
	}
	select {
	case <-requests.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReservedHost reports whether a startup route already uses the host.
func (h *Handler) ReservedHost(host string) bool {
	host = CanonicalHost(host)
	for _, route := range h.staticRoutes {
		if CanonicalHost(route.Host) == host {
			return true
		}
	}
	return false
}

// CanonicalHost normalizes a request or route host for matching.
func CanonicalHost(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
