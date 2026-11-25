//go:build integration

package database_test

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
)

func TestStaticSiteVersionHTTP(t *testing.T) {
	ctx := t.Context()
	store, connection, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "integration-account-password"
	owners := []string{"site-owner@cloudlet.local", "other-owner@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, owners, password)
	dataDirectory := t.TempDir()
	newAPI := func(db *database.Store) (*httptest.Server, *staticdeploy.Service, func()) {
		t.Helper()
		sites, err := staticdeploy.New(db, dataDirectory)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewUnstartedServer(nil)
		handler, cleanup, err := controlplane.NewHandlerWithMCP(db, sites, nil,
			config.Config{Instance: "integration", PublicOrigin: "http://localhost"}, slog.Default(), "http://"+server.Listener.Addr().String())
		if err != nil {
			server.Close()
			t.Fatal(errors.Join(err, sites.Close()))
		}
		server.Config.Handler = handler
		server.Start()
		return server, sites, func() {
			server.Close()
			cleanup()
			if err := sites.Close(); err != nil {
				t.Error(err)
			}
		}
	}
	server, sites, closeAPI := newAPI(store)
	defer func() { closeAPI() }()
	ownerHTTP := newSessionClient(t)
	otherHTTP := newSessionClient(t)
	ownerAPI := authenticatedAPI(t, server.URL, ownerHTTP, owners[0], password)
	otherAPI := authenticatedAPI(t, server.URL, otherHTTP, owners[1], password)
	const siteName = "static-app"
	zipBytes := siteZIP(t, "version one")
	upload := func(api *apigen.ClientWithResponses, project, name, contentType string, body io.Reader, want int) *apigen.UploadStaticSiteVersionResponse {
		t.Helper()
		response, err := api.UploadStaticSiteVersionWithBodyWithResponse(ctx, project, name, contentType, body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != want {
			t.Fatalf("upload %s/%s = %d %s, want %d", project, name, response.StatusCode(), response.Body, want)
		}
		return response
	}
	upload(otherAPI, owner.ID, siteName, "application/zip", bytes.NewReader(zipBytes), http.StatusNotFound)
	upload(ownerAPI, owner.ID, siteName, "application/octet-stream", bytes.NewReader(zipBytes), http.StatusUnsupportedMediaType)
	upload(ownerAPI, owner.ID, "Invalid", "application/zip", bytes.NewReader(zipBytes), http.StatusBadRequest)
	upload(ownerAPI, owner.ID, siteName, "application/zip", bytes.NewReader([]byte("broken ZIP")), http.StatusBadRequest)
	tooLong := siteZIP(t, "valid index", strings.Repeat("界", 86)+"/file.txt")
	upload(ownerAPI, owner.ID, siteName, "application/zip", bytes.NewReader(tooLong), http.StatusBadRequest)
	assertSiteCount(t, store, 0, 0)
	assertVersionDirectoryCount(t, dataDirectory, 0)

	// Valid files with a database foreign-key failure must also be cleaned up.
	if _, err := sites.Upload(ctx, uuid.NewString(), siteName, bytes.NewReader(zipBytes)); err == nil {
		t.Fatal("upload to a non-existing project succeeded")
	}
	assertSiteCount(t, store, 0, 0)
	assertVersionDirectoryCount(t, dataDirectory, 0)

	first := upload(ownerAPI, owner.ID, siteName, "application/zip", bytes.NewReader(zipBytes), http.StatusCreated)
	version := first.JSON201
	digest := fmt.Sprintf("%x", sha256.Sum256(zipBytes))
	if version == nil || version.Id == uuid.Nil || version.SiteId == uuid.Nil || version.ArtifactDigest != digest || version.Status != apigen.Ready || version.CreatedAt.IsZero() {
		t.Fatalf("wrong version metadata: %+v", version)
	}
	wantLocation := "/api/v1/projects/" + owner.ID + "/sites/" + siteName + "/versions/" + version.Id.String()
	if location := first.HTTPResponse.Header.Get("Location"); location != wantLocation {
		t.Fatalf("Location = %q, want %q", location, wantLocation)
	}

	second := upload(ownerAPI, owner.ID, siteName, "application/zip", bytes.NewReader(siteZIP(t, "version two")), http.StatusCreated).JSON201
	if second == nil || second.SiteId != version.SiteId || second.Id == version.Id || second.ArtifactDigest == version.ArtifactDigest {
		t.Fatalf("second upload did not create a new version for the same site: %+v", second)
	}
	downloaded, err := os.ReadFile(filepath.Join(dataDirectory, "archives", digest))
	if err != nil || !bytes.Equal(downloaded, zipBytes) {
		t.Fatalf("original ZIP differs: %v", err)
	}
	upload(ownerAPI, owner.ID, siteName, "application/zip", bytes.NewReader([]byte("broken ZIP")), http.StatusBadRequest)
	assertSiteCount(t, store, 1, 2)
	assertVersionDirectoryCount(t, dataDirectory, 2)
	assertAPIVersion(t, otherAPI, owner.ID, siteName, version.Id, http.StatusNotFound)
	assertAPIVersion(t, otherAPI, other.ID, siteName, version.Id, http.StatusNotFound)
	assertAPIVersion(t, ownerAPI, owner.ID, "other-site", version.Id, http.StatusNotFound)
	viewer, err := store.UserByEmail(ctx, owners[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, owner.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	assertAPIVersion(t, otherAPI, owner.ID, siteName, version.Id, http.StatusOK)
	upload(otherAPI, owner.ID, siteName, "application/zip", bytes.NewReader(zipBytes), http.StatusForbidden)

	type result struct {
		response *apigen.UploadStaticSiteVersionResponse
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			response, err := ownerAPI.UploadStaticSiteVersionWithBodyWithResponse(ctx, owner.ID, "concurrent", "application/zip", bytes.NewReader(zipBytes))
			results <- result{response, err}
		}()
	}
	var concurrent []*apigen.StaticSiteVersion
	for range 2 {
		result := <-results
		if result.err != nil || result.response.JSON201 == nil {
			t.Fatalf("concurrent upload = %+v, %v", result.response, result.err)
		}
		concurrent = append(concurrent, result.response.JSON201)
	}
	if concurrent[0].SiteId != concurrent[1].SiteId || concurrent[0].Id == concurrent[1].Id {
		t.Fatalf("uploads must share a site identity and use distinct versions: %v", concurrent)
	}
	assertSiteCount(t, store, 2, 4)
	assertVersionDirectoryCount(t, dataDirectory, 4)

	closeAPI()
	closeAPI = func() {}
	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.SQL.Close(); err != nil {
			t.Error(err)
		}
	})
	server, sites, closeAPI = newAPI(reopened)
	ownerAPI, err = apigen.NewClientWithResponses(server.URL, apigen.WithHTTPClient(ownerHTTP))
	if err != nil {
		t.Fatal(err)
	}
	loaded := assertAPIVersion(t, ownerAPI, owner.ID, siteName, version.Id, http.StatusOK)
	if loaded.SiteId != version.SiteId || loaded.ArtifactDigest != digest || loaded.Status != apigen.Ready {
		t.Fatalf("metadata changed after restart: %+v", loaded)
	}
	record, err := sites.Version(ctx, owner.ID, siteName, version.Id)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := sites.Handler(record)
	if err != nil {
		t.Fatal(err)
	}
	files := httptest.NewServer(handler)
	defer files.Close()
	response, err := files.Client().Get(files.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "version one" {
		t.Fatalf("site version files after restart = (%d, %q, %v)", response.StatusCode, body, err)
	}
}

func makeProjectOwners(t *testing.T, store *database.Store, emails []string, password string) (database.Project, database.Project) {
	t.Helper()
	projects := make([]database.Project, 0, len(emails))
	for _, email := range emails {
		if err := store.SeedUser(t.Context(), email, password); err != nil {
			t.Fatal(err)
		}
		user, err := store.Authenticate(t.Context(), email, password)
		if err != nil {
			t.Fatal(err)
		}
		project, err := store.CreateProject(t.Context(), user.ID, email)
		if err != nil {
			t.Fatal(err)
		}
		projects = append(projects, project)
	}
	return projects[0], projects[1]
}

func authenticatedAPI(t *testing.T, url string, httpClient *http.Client, email, password string) *apigen.ClientWithResponses {
	t.Helper()
	client, err := apigen.NewClientWithResponses(url, apigen.WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.LoginWithResponse(t.Context(), apigen.LoginJSONRequestBody{Email: email, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK {
		t.Fatalf("login returned %d: %s", response.StatusCode(), response.Body)
	}
	return client
}

func assertAPIVersion(t *testing.T, client *apigen.ClientWithResponses, projectID, name string, id uuid.UUID, want int) *apigen.StaticSiteVersion {
	t.Helper()
	response, err := client.GetStaticSiteVersionWithResponse(t.Context(), projectID, name, id.String())
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != want {
		t.Fatalf("get version = %d %s, want %d", response.StatusCode(), response.Body, want)
	}
	return response.JSON200
}

func siteZIP(t *testing.T, html string, extraPaths ...string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, err := writer.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, html); err != nil {
		t.Fatal(err)
	}
	for _, path := range extraPaths {
		if _, err := writer.Create(path); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func assertSiteCount(t *testing.T, store *database.Store, sites, versions int64) {
	t.Helper()
	for table, want := range map[string]int64{"static_sites": sites, "static_site_versions": versions} {
		var count int64
		if err := store.DB.Table(table).Count(&count).Error; err != nil || count != want {
			t.Fatalf("%s count = (%d, %v), want %d", table, count, err, want)
		}
	}
}

func assertVersionDirectoryCount(t *testing.T, root string, want int) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "versions"))
	if err != nil || len(entries) != want {
		t.Fatalf("version directories = %v, %v, want %d", entries, err, want)
	}
}
