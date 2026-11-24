package gateway

import (
	"cmp"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
)

type pathRoute struct {
	segments []string
	proxy    *httputil.ReverseProxy
}

type routeSnapshot map[string][]pathRoute

// Handler serves requests using the current immutable route snapshot.
type Handler struct {
	staticRoutes []config.Route
	transport    *http.Transport
	logger       *slog.Logger
	snapshot     atomic.Pointer[routeSnapshot]
}

func NewHandler(cfg config.Config, logger *slog.Logger) (*Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	h := &Handler{staticRoutes: slices.Clone(cfg.Routes), transport: transport, logger: logger}
	if err := h.ReplaceRoutes(nil); err != nil {
		return nil, err
	}
	return h, nil
}

// ReplaceRoutes atomically replaces all dynamic routes and preserves startup routes.
// A dynamic route cannot use a host reserved by startup configuration.
func (h *Handler) ReplaceRoutes(routes []config.Route) error {
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
	proxies := make(routeSnapshot, len(combined))
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
		proxies[host] = append(proxies[host], pathRoute{segments: segments, proxy: proxy})
	}
	for _, routes := range proxies {
		slices.SortFunc(routes, func(a, b pathRoute) int {
			return cmp.Compare(len(b.segments), len(a.segments))
		})
	}
	h.snapshot.Store(&proxies)
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	routes := (*h.snapshot.Load())[CanonicalHost(r.Host)]
	if len(routes) > 0 {
		if len(routes[0].segments) == 0 {
			routes[0].proxy.ServeHTTP(w, r)
			return
		}
		// Split before decoding so an escaped slash stays in its original segment.
		segments := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
		for i, segment := range segments {
			decoded, err := url.PathUnescape(segment)
			if err != nil {
				http.Error(w, "invalid request path", http.StatusBadRequest)
				return
			}
			if decoded == "." || decoded == ".." {
				http.Error(w, "invalid request path", http.StatusBadRequest)
				return
			}
			segments[i] = decoded
		}
		for _, route := range routes {
			if n := len(route.segments); n <= len(segments) && slices.Equal(route.segments, segments[:n]) {
				route.proxy.ServeHTTP(w, r)
				return
			}
		}
	}
	http.NotFound(w, r)
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
