package sitehttp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/sitehttp"
)

func TestSPAFallbackWithRealHTTP(t *testing.T) {
	enabled := newSiteWithOptions(t, sitehttp.Options{SPAFallback: true})
	disabled := newSite(t)
	headers := http.Header{"Accept": {"text/html"}, "Sec-Fetch-Mode": {"navigate"}, "Sec-Fetch-Dest": {"document"}}
	for _, test := range []struct {
		name, method, path string
		status             int
		body               string
	}{
		{"deep route", "GET", "/dashboard/settings?tab=team", 200, "<h1>Home</h1>"},
		{"deep route with trailing slash", "GET", "/dashboard/settings/", 200, "<h1>Home</h1>"},
		{"head", "HEAD", "/dashboard/settings", 200, ""},
		{"existing file", "GET", "/assets/plain", 200, "hello"},
		{"missing script", "GET", "/assets/missing.js", 404, ""},
		{"existing directory without index", "GET", "/assets/", 404, ""},
		{"unsafe path", "GET", "/%2e%2e/settings", 404, ""},
		{"unsupported method", "POST", "/dashboard/settings", 405, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := requestWithHeaders(t, enabled, test.method, test.path, headers)
			if response.StatusCode != test.status || (test.status == 200 && body != test.body) {
				t.Fatalf("response = %d %q, want %d %q", response.StatusCode, body, test.status, test.body)
			}
			if test.method == "HEAD" && (!strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") || response.ContentLength != int64(len("<h1>Home</h1>"))) {
				t.Fatalf("incorrect fallback representation headers: %v", response.Header)
			}
		})
	}
	response, _ := requestWithHeaders(t, disabled, "GET", "/dashboard/settings", headers)
	if response.StatusCode != 404 {
		t.Fatalf("disabled fallback returned %d", response.StatusCode)
	}
	for _, requestHeaders := range []http.Header{
		{"Accept": {"*/*"}},
		{"Accept": {"text/html"}, "Sec-Fetch-Mode": {"cors"}},
		{"Accept": {"text/html;q=0, application/json"}},
	} {
		response, _ := requestWithHeaders(t, enabled, "GET", "/dashboard/settings", requestHeaders)
		if response.StatusCode != 404 || response.Header.Get("Vary") != "Accept, Sec-Fetch-Mode, Sec-Fetch-Dest" {
			t.Fatalf("non-navigation response = %d, Vary = %q", response.StatusCode, response.Header.Get("Vary"))
		}
	}
}
