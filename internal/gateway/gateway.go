package gateway

import (
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

type routeSnapshot map[string]*httputil.ReverseProxy

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
// A dynamic route cannot use a host already reserved by a startup route.
func (h *Handler) ReplaceRoutes(routes []config.Route) error {
	combined := slices.Concat(h.staticRoutes, routes)
	if err := config.ValidateRoutes(combined); err != nil {
		return err
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
				h.logger.Error("proxy request failed", "host", r.Host, "error", err)
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			},
		}
		proxies[canonicalHost(route.Host)] = proxy
	}
	h.snapshot.Store(&proxies)
	return nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proxy, found := (*h.snapshot.Load())[canonicalHost(r.Host)]
	if !found {
		http.NotFound(w, r)
		return
	}
	proxy.ServeHTTP(w, r)
}

func canonicalHost(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
