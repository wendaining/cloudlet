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
	"time"

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

	t.Run("switch and restore published versions", func(t *testing.T) {
		type activePublication struct {
			VersionID uuid.UUID `json:"version_id"`
			Hostname  string    `json:"hostname"`
			URL       string    `json:"url"`
			Revision  time.Time `json:"revision"`
		}
		sitePath := api.URL + "/api/v1/projects/" + owner.ID + "/sites/site"
		activateURL := func(version uuid.UUID) string {
			return sitePath + "/versions/" + version.String() + "/activate"
		}
		decode := func(response *http.Response) activePublication {
			t.Helper()
			defer response.Body.Close()
			var result activePublication
			if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		read := func(client *http.Client) activePublication {
			t.Helper()
			return decode(memberRequest(t, client, http.MethodGet, sitePath+"/publication", nil, http.StatusOK))
		}
		activate := func(client *http.Client, id uuid.UUID, revision time.Time, want int) activePublication {
			t.Helper()
			response := memberRequest(t, client, http.MethodPost, activateURL(id), map[string]time.Time{"expected_revision": revision}, want)
			if want != http.StatusOK {
				response.Body.Close()
				return activePublication{}
			}
			return decode(response)
		}

		a, b := first.JSON201.Id, second.JSON201.Id
		initial := read(otherHTTP)
		if initial.VersionID != a || initial.Hostname != host || initial.URL != wantURL || initial.Revision.IsZero() {
			t.Fatalf("unexpected first publication: %+v", initial)
		}
		activate(otherHTTP, b, initial.Revision, http.StatusForbidden)
		activate(ownerHTTP, otherVersion.JSON201.Id, initial.Revision, http.StatusNotFound)
		activate(ownerHTTP, b, time.Time{}, http.StatusBadRequest)
		switched := activate(ownerHTTP, b, initial.Revision, http.StatusOK)
		if switched.VersionID != b || switched.Hostname != host || switched.URL != wantURL || !switched.Revision.After(initial.Revision) {
			t.Fatalf("unexpected switched publication: %+v", switched)
		}
		check(front, host, "/", http.StatusOK, "second")
		check(front, host, "/private.css", http.StatusOK, "")

		replay := activate(ownerHTTP, b, initial.Revision, http.StatusOK)
		if replay != switched {
			t.Fatalf("retry changed publication: %+v != %+v", replay, switched)
		}
		if snapshot, _, err := store.RouteSnapshot(t.Context()); err != nil || snapshot != activeSnapshot+1 {
			t.Fatalf("switch and retry advanced the snapshot to %d: %v", snapshot, err)
		}
		activate(ownerHTTP, a, initial.Revision, http.StatusConflict)
		broken := uploadPublicationVersion(t, ownerAPI, owner.ID, "broken")
		brokenVersion, err := sites.Version(t.Context(), owner.ID, "site", broken)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(storage, "versions", brokenVersion.Directory, "index.html")); err != nil {
			t.Fatal(err)
		}
		activate(ownerHTTP, broken, switched.Revision, http.StatusConflict)
		check(front, host, "/", http.StatusOK, "second")
		if current := read(ownerHTTP); current != switched {
			t.Fatalf("failed switches changed publication: %+v", current)
		}

		restored := activate(ownerHTTP, a, switched.Revision, http.StatusOK)
		if restored.VersionID != a || !restored.Revision.After(switched.Revision) {
			t.Fatalf("unexpected restored publication: %+v", restored)
		}
		check(front, host, "/", http.StatusOK, "first")
		activate(ownerHTTP, b, initial.Revision, http.StatusConflict)

		third := uploadPublicationVersion(t, ownerAPI, owner.ID, "third")
		type result struct {
			code   int
			target uuid.UUID
			err    error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		payload, err := json.Marshal(map[string]time.Time{"expected_revision": restored.Revision})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []uuid.UUID{b, third} {
			go func() {
				<-start
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, activateURL(id), bytes.NewReader(payload))
				if err != nil {
					results <- result{err: err}
					return
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := ownerHTTP.Do(request)
				if err != nil {
					results <- result{err: err}
					return
				}
				response.Body.Close()
				results <- result{code: response.StatusCode, target: id}
			}()
		}
		close(start)
		var winner uuid.UUID
		var failures int
		for range 2 {
			outcome := <-results
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			switch outcome.code {
			case http.StatusOK:
				winner = outcome.target
			case http.StatusConflict:
				failures++
			default:
				t.Fatalf("unexpected concurrent status %d", outcome.code)
			}
		}
		if failures != 1 || winner == uuid.Nil || read(ownerHTTP).VersionID != winner {
			t.Fatalf("unexpected concurrent result: winner=%s conflicts=%d", winner, failures)
		}
		if snapshot, _, err := store.RouteSnapshot(t.Context()); err != nil || snapshot != activeSnapshot+3 {
			t.Fatalf("unexpected snapshot after concurrent switches: %d (%v)", snapshot, err)
		}
		want := "second"
		if winner == third {
			want = "third"
		}
		check(front, host, "/", http.StatusOK, want)
		recovered, _ := newFront()
		check(recovered, host, "/", http.StatusOK, want)
		check(recovered, "worker.localhost", "/", http.StatusOK, "existing project route")
	})
}

func uploadPublicationVersion(t *testing.T, client *apigen.ClientWithResponses, project, content string) uuid.UUID {
	t.Helper()
	response, err := client.UploadStaticSiteVersionWithBodyWithResponse(t.Context(), project, "site", "application/zip", bytes.NewReader(siteZIP(t, content)))
	if err != nil || response.JSON201 == nil {
		t.Fatalf("upload %s: %v, %v", content, response, err)
	}
	return response.JSON201.Id
}
