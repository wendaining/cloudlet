package gateway

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/wendaining/cloudlet/internal/config"
)

func NewHandler(cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	proxies := make(map[string]*httputil.ReverseProxy)
	for _, route := range cfg.Routes {
		target, err := url.Parse(route.Target)
		if err != nil {
			return nil, err
		}
		proxy := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(request *httputil.ProxyRequest) {
				request.SetURL(target)
				request.SetXForwarded()
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
				logger.Error("proxy request failed", "host", r.Host, "error", err)
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
			},
		}
		proxies[canonicalHost(route.Host)] = proxy
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy, found := proxies[canonicalHost(r.Host)]
		if !found {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}), nil
}

func canonicalHost(host string) string {
	if hostname, _, err := net.SplitHostPort(host); err == nil {
		host = hostname
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
