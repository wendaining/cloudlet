package sitehttp_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wendaining/cloudlet/internal/sitehttp"
)

func TestStaticFileRevalidationWithRealHTTP(t *testing.T) {
	directory := t.TempDir()
	const initial = "body { color: red; }"
	const updated = "body { color: tan; }"
	filename := filepath.Join(directory, "app.css")
	modified := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	for path, content := range map[string]string{filename: initial, filepath.Join(directory, "index.html"): "<h1>Home</h1>"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := sitehttp.New(os.DirFS(directory))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	homepage, _ := request(t, server, "GET", "/")
	if policy := homepage.Header.Get("Cache-Control"); policy != "no-cache" || homepage.Header.Get("ETag") == "" {
		t.Fatalf("HTML response has unexpected validator or policy: %v", homepage.Header)
	}
	asset, body := request(t, server, "GET", "/app.css")
	tag := fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(initial)))
	if asset.StatusCode != 200 || body != initial || asset.Header.Get("ETag") != tag ||
		asset.Header.Get("Last-Modified") != modified.Format(http.TimeFormat) || asset.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("unexpected asset response: %d %v %q", asset.StatusCode, asset.Header, body)
	}
	for _, test := range []struct {
		name, method string
		headers      http.Header
	}{
		{"strong tag", "GET", http.Header{"If-None-Match": {tag}}},
		{"weak tag", "GET", http.Header{"If-None-Match": {"W/" + tag}}},
		{"HEAD version", "HEAD", http.Header{"If-None-Match": {tag}}},
		{"last modified", "GET", http.Header{"If-Modified-Since": {modified.Format(http.TimeFormat)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, body := cacheRequest(t, server, test.method, "/app.css", test.headers)
			if response.StatusCode != 304 || body != "" || response.Header.Get("ETag") != tag ||
				response.Header.Get("Content-Type") != "" || response.Header.Get("Cache-Control") != "public, max-age=300" {
				t.Fatalf("conditional response = %d %v %q", response.StatusCode, response.Header, body)
			}
		})
	}
	if err := os.WriteFile(filename, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filename, modified, modified); err != nil {
		t.Fatal(err)
	}
	response, body := cacheRequest(t, server, "GET", "/app.css", http.Header{
		"If-None-Match": {tag}, "If-Modified-Since": {modified.Format(http.TimeFormat)},
	})
	if response.StatusCode != 200 || body != updated || response.Header.Get("ETag") != fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(updated))) {
		t.Fatalf("updated content response = %d %v %q", response.StatusCode, response.Header, body)
	}
	missing, _ := cacheRequest(t, server, "GET", "/missing.css", http.Header{"If-None-Match": {"*"}})
	if missing.StatusCode != 404 || missing.Header.Get("ETag") != "" {
		t.Fatalf("missing file response = %d %v", missing.StatusCode, missing.Header)
	}
}

func TestServeNonSeekableZIPFile(t *testing.T) {
	const content = "body {color: green;}"
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	file, err := writer.Create("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := sitehttp.New(archive)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, body := request(t, server, "GET", "/style.css")
	if response.StatusCode != 200 || body != content || response.Header.Get("ETag") != fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(content))) {
		t.Fatalf("archive file response = %d %v %q", response.StatusCode, response.Header, body)
	}
}

func cacheRequest(t *testing.T, server *httptest.Server, method, path string, headers http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header = headers
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response, string(body)
}
