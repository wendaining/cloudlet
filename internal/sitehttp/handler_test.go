package sitehttp_test

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/sitehttp"
)

func TestServeStaticSite(t *testing.T) {
	server := newSite(t)
	for _, test := range []struct {
		name, method, path, body, mediaType string
		status                              int
	}{
		{"homepage", "GET", "/", "<h1>Home</h1>", "text/html", http.StatusOK},
		{"directory index", "GET", "/guide/", "<h1>Guide</h1>", "text/html", http.StatusOK},
		{"stylesheet", "GET", "/assets/app.css?rev=2", "body { color: navy; }", "text/css", http.StatusOK},
		{"sniff contents", "GET", "/assets/plain", "hello", "text/plain", http.StatusOK},
		{"head", "HEAD", "/assets/app.css", "", "text/css", http.StatusOK},
		{"missing file", "GET", "/missing", "", "", http.StatusNotFound},
		{"missing index", "GET", "/assets/", "", "", http.StatusNotFound},
		{"missing index without slash", "GET", "/assets", "", "", http.StatusNotFound},
		{"nonregular index", "GET", "/broken/", "", "", http.StatusNotFound},
		{"file with slash", "GET", "/assets/app.css/", "", "", http.StatusNotFound},
		{"directory list traversal", "GET", "/assets/../", "", "", http.StatusNotFound},
		{"encoded traversal", "GET", "/%2e%2e/index.html", "", "", http.StatusNotFound},
		{"dot alias", "GET", "/./", "", "", http.StatusNotFound},
		{"unsupported method", "POST", "/", "", "", http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := request(t, server, test.method, test.path)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if test.mediaType != "" && !strings.HasPrefix(response.Header.Get("Content-Type"), test.mediaType) {
				t.Fatalf("content type = %q, want %q", response.Header.Get("Content-Type"), test.mediaType)
			}
			if test.status == http.StatusOK && body != test.body {
				t.Fatalf("body = %q, want %q", body, test.body)
			}
			if test.method == "HEAD" && response.ContentLength != int64(len("body { color: navy; }")) {
				t.Fatalf("HEAD content length = %s", strconv.FormatInt(response.ContentLength, 10))
			}
			if test.status == http.StatusNotFound && strings.Contains(body, "app.css") {
				t.Fatalf("404 response contains a directory listing: %q", body)
			}
			if test.status == http.StatusMethodNotAllowed && response.Header.Get("Allow") != "GET, HEAD" {
				t.Fatalf("Allow = %q", response.Header.Get("Allow"))
			}
		})
	}
}

func TestDirectoryRedirectPreservesQuery(t *testing.T) {
	server := newSite(t)
	response, _ := request(t, server, "GET", "/guide?source=home")
	if response.StatusCode != http.StatusMovedPermanently || response.Header.Get("Location") != "/guide/?source=home" {
		t.Fatalf("redirect = (%d, %q)", response.StatusCode, response.Header.Get("Location"))
	}
}

func TestHandlerRequiresFilesystem(t *testing.T) {
	if handler, err := sitehttp.New(nil); handler != nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("New(nil) = (%v, %v), want configuration error", handler, err)
	}
}

func newSite(t *testing.T) *httptest.Server {
	t.Helper()
	directory := t.TempDir()
	for name, content := range map[string]string{
		"index.html":            "<h1>Home</h1>",
		"guide/index.html":      "<h1>Guide</h1>",
		"assets/app.css":        "body { color: navy; }",
		"assets/plain":          "hello",
		"broken/index.html/foo": "",
	} {
		filename := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := sitehttp.New(os.DirFS(directory))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	server.Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return server
}

func request(t *testing.T, server *httptest.Server, method, path string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	if err := errors.Join(readErr, response.Body.Close()); err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}
