//go:build integration

package database_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
)

func TestFirstStaticSitePublicationHTTP(t *testing.T) {
	store, _, _ := openIntegrationDatabase(t, t.Context())
	if err := store.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	const password = "site-publish-integration-password"
	emails := []string{"publish@cloudlet.local", "other-publish@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	storage := t.TempDir()
	sites, err := staticdeploy.New(store, storage)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sites.Close(); err != nil {
			t.Error(err)
		}
	})

	upstreamFiles := t.TempDir()
	if err := os.WriteFile(filepath.Join(upstreamFiles, "index.html"), []byte("existing project route"), 0o600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.FileServer(http.Dir(upstreamFiles)))
	t.Cleanup(upstream.Close)
	cfg := config.Config{
		Instance: "site-publish", PublicOrigin: "http://localhost",
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: "reserved.localhost", Target: upstream.URL}},
	}
	newFront := func() (*httptest.Server, *routes.Publisher) {
		t.Helper()
		front, err := gateway.NewHandler(cfg, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		publisher, err := routes.NewPublisherWithSites(store, front, sites.Handler)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(front)
		t.Cleanup(server.Close)
		return server, publisher
	}
	front, publisher := newFront()
	if _, err := publisher.PublishProject(t.Context(), owner.ID, []routes.Route{{
		Resource: "worker", Host: "worker.localhost", Target: upstream.URL,
	}}); err != nil {
		t.Fatal(err)
	}

	cfg.GatewayAddress = front.Listener.Addr().String()
	api := httptest.NewUnstartedServer(nil)
	handler, cleanup, err := controlplane.NewHandlerWithMCPAndWorkers(store, sites, nil, cfg, slog.Default(), "http://"+api.Listener.Addr().String(), nil, publisher)
	if err != nil {
		api.Close()
		t.Fatal(err)
	}
	api.Config.Handler = handler
	api.Start()
	t.Cleanup(func() { api.Close(); cleanup() })
	ownerHTTP, otherHTTP := newSessionClient(t), newSessionClient(t)
	ownerAPI := authenticatedAPI(t, api.URL, ownerHTTP, emails[0], password)
	otherAPI := authenticatedAPI(t, api.URL, otherHTTP, emails[1], password)
	first, err := ownerAPI.UploadStaticSiteVersionWithBodyWithResponse(t.Context(), owner.ID, "site", "application/zip", bytes.NewReader(siteZIP(t, "first", "app.css")))
	if err != nil || first.JSON201 == nil {
		t.Fatalf("upload first version: %v, %v", first, err)
	}
	second, err := ownerAPI.UploadStaticSiteVersionWithBodyWithResponse(t.Context(), owner.ID, "site", "application/zip", bytes.NewReader(siteZIP(t, "second", "private.css")))
	if err != nil || second.JSON201 == nil {
		t.Fatalf("upload second version: %v, %v", second, err)
	}
	otherVersion, err := otherAPI.UploadStaticSiteVersionWithBodyWithResponse(t.Context(), other.ID, "site", "application/zip", bytes.NewReader(siteZIP(t, "other")))
	if err != nil || otherVersion.JSON201 == nil {
		t.Fatalf("upload other project version: %v, %v", otherVersion, err)
	}

	publish := func(client *http.Client, project string, version uuid.UUID, hostname string, want int) string {
		t.Helper()
		endpoint := api.URL + "/api/v1/projects/" + project + "/sites/site/versions/" + version.String() + "/publish"
		response := memberRequest(t, client, http.MethodPost, endpoint, map[string]string{"hostname": hostname}, want)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	check := func(server *httptest.Server, host, path string, status int, want string) {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || string(body) != want {
			t.Fatalf("GET %s%s: got %d and %q (%v), want %d and %q", host, path, response.StatusCode, body, err, status, want)
		}
	}
	const host = "site.localhost"
	const notFound = "404 page not found\n"
	check(front, host, "/", http.StatusNotFound, notFound)
	publish(ownerHTTP, owner.ID, uuid.New(), host, http.StatusNotFound)
	publish(otherHTTP, owner.ID, first.JSON201.Id, host, http.StatusNotFound)
	publish(ownerHTTP, owner.ID, otherVersion.JSON201.Id, host, http.StatusNotFound)
	publish(newSessionClient(t), owner.ID, first.JSON201.Id, host, http.StatusUnauthorized)
	publish(ownerHTTP, owner.ID, first.JSON201.Id, "reserved.localhost", http.StatusConflict)
	publish(ownerHTTP, owner.ID, first.JSON201.Id, "worker.localhost", http.StatusConflict)
	publish(ownerHTTP, owner.ID, first.JSON201.Id, "bad.localhost:80", http.StatusBadRequest)
	answer := publish(ownerHTTP, owner.ID, first.JSON201.Id, "SITE.localhost.", http.StatusOK)
	var publication apigen.StaticSitePublication
	base, err := url.Parse(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://" + host + ":" + base.Port() + "/"
	if err := json.Unmarshal([]byte(answer), &publication); err != nil ||
		publication.SiteId != first.JSON201.SiteId || publication.VersionId != first.JSON201.Id ||
		publication.Hostname != host || publication.Url != wantURL {
		t.Fatalf("publish returned %s (%v), expected activated version and %s", answer, err, wantURL)
	}
	activeSnapshot, _, err := store.RouteSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	publish(ownerHTTP, owner.ID, first.JSON201.Id, host, http.StatusOK)
	if snapshot, _, err := store.RouteSnapshot(t.Context()); err != nil || snapshot != activeSnapshot {
		t.Fatalf("retry changed snapshot from %d to %d: %v", activeSnapshot, snapshot, err)
	}
	publish(ownerHTTP, owner.ID, second.JSON201.Id, host, http.StatusConflict)
	publish(otherHTTP, other.ID, otherVersion.JSON201.Id, host, http.StatusConflict)
	version, err := sites.Version(t.Context(), other.ID, "site", otherVersion.JSON201.Id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(storage, "versions", version.Directory, "index.html")); err != nil {
		t.Fatal(err)
	}
	publish(otherHTTP, other.ID, otherVersion.JSON201.Id, "unready.localhost", http.StatusConflict)
	check(front, "unready.localhost", "/", http.StatusNotFound, notFound)
	viewer, err := store.Authenticate(t.Context(), emails[1], password)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(t.Context(), owner.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	publish(otherHTTP, owner.ID, first.JSON201.Id, host, http.StatusForbidden)
	check(front, host, "/", http.StatusOK, "first")
	check(front, host, "/app.css", http.StatusOK, "")
	check(front, host, "/private.css", http.StatusNotFound, notFound)
	check(front, host, "/api/v1/version", http.StatusNotFound, notFound)
	check(front, "worker.localhost", "/", http.StatusOK, "existing project route")

	restarted, _ := newFront()
	check(restarted, host, "/", http.StatusOK, "first")
	check(restarted, "worker.localhost", "/", http.StatusOK, "existing project route")
}
