//go:build integration

package database_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
)

type staticSiteListItem struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

func TestStaticSiteListsHTTP(t *testing.T) {
	ctx := t.Context()
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "site-list-test-password"
	const ownerEmail, otherEmail = "site-list-owner@cloudlet.local", "site-list-other@cloudlet.local"
	owner, other := makeProjectOwners(t, store, []string{ownerEmail, otherEmail}, password)
	sites, err := staticdeploy.New(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sites.Close(); err != nil {
			t.Error(err)
		}
	})
	server := httptest.NewUnstartedServer(nil)
	handler, cleanup, err := controlplane.NewHandlerWithMCP(store, sites, nil,
		config.Config{Instance: "site-list", PublicOrigin: "http://localhost"}, slog.Default(), "http://"+server.Listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(func() { server.Close(); cleanup() })
	ownerHTTP, otherHTTP := newSessionClient(t), newSessionClient(t)
	ownerAPI := authenticatedAPI(t, server.URL, ownerHTTP, ownerEmail, password)
	otherAPI := authenticatedAPI(t, server.URL, otherHTTP, otherEmail, password)
	ownerPath := server.URL + "/api/v1/projects/" + owner.ID + "/sites"
	otherPath := server.URL + "/api/v1/projects/" + other.ID + "/sites"

	sitesPage := func(client *http.Client, endpoint string) ([]staticSiteListItem, string) {
		t.Helper()
		response := memberRequest(t, client, http.MethodGet, endpoint, nil, http.StatusOK)
		defer response.Body.Close()
		var page struct {
			Sites      []staticSiteListItem `json:"sites"`
			NextCursor string               `json:"next_cursor"`
		}
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil || page.Sites == nil {
			t.Fatalf("invalid site list %+v: %v", page, err)
		}
		return page.Sites, page.NextCursor
	}
	versionsPage := func(client *http.Client, endpoint string) ([]apigen.StaticSiteVersion, string) {
		t.Helper()
		response := memberRequest(t, client, http.MethodGet, endpoint, nil, http.StatusOK)
		defer response.Body.Close()
		var page struct {
			Versions   []apigen.StaticSiteVersion `json:"versions"`
			NextCursor string                     `json:"next_cursor"`
		}
		if err := json.NewDecoder(response.Body).Decode(&page); err != nil || page.Versions == nil {
			t.Fatalf("invalid version list %+v: %v", page, err)
		}
		return page.Versions, page.NextCursor
	}
	upload := func(client *apigen.ClientWithResponses, project, name, content string) apigen.StaticSiteVersion {
		t.Helper()
		response, err := client.UploadStaticSiteVersionWithBodyWithResponse(ctx, project, name, "application/zip", bytes.NewReader(siteZIP(t, content)))
		if err != nil || response.JSON201 == nil {
			t.Fatalf("upload %s/%s: %v, %v", project, name, response, err)
		}
		return *response.JSON201
	}

	if rows, cursor := sitesPage(ownerHTTP, ownerPath); len(rows) != 0 || cursor != "" {
		t.Fatalf("empty project returned %v, %q", rows, cursor)
	}
	first := upload(ownerAPI, owner.ID, "alpha", "first")
	second := upload(ownerAPI, owner.ID, "alpha", "second")
	bravo := upload(ownerAPI, owner.ID, "bravo", "bravo")
	isolated := upload(otherAPI, other.ID, "alpha", "other")
	// Exercise the UUID tie-breaker with equal PostgreSQL timestamps.
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	if err := store.DB.WithContext(ctx).Exec("UPDATE static_site_versions SET created_at = ? WHERE id IN ?", stamp, []uuid.UUID{first.Id, second.Id}).Error; err != nil {
		t.Fatal(err)
	}

	rows, cursor := sitesPage(ownerHTTP, ownerPath+"?limit=1")
	if len(rows) != 1 || rows[0].ID != first.SiteId || rows[0].Name != "alpha" || cursor == "" {
		t.Fatalf("site first page: %v, %q", rows, cursor)
	}
	rows, cursor = sitesPage(ownerHTTP, ownerPath+"?limit=1&cursor="+url.QueryEscape(cursor))
	if len(rows) != 1 || rows[0].ID != bravo.SiteId || rows[0].Name != "bravo" || cursor != "" {
		t.Fatalf("site second page: %v, %q", rows, cursor)
	}

	ordered := []apigen.StaticSiteVersion{first, second}
	slices.SortFunc(ordered, func(a, b apigen.StaticSiteVersion) int { return strings.Compare(b.Id.String(), a.Id.String()) })
	endpoint := ownerPath + "/alpha/versions"
	versions, cursor := versionsPage(ownerHTTP, endpoint+"?limit=1")
	if len(versions) != 1 || versions[0].Id != ordered[0].Id || versions[0].ArtifactDigest != ordered[0].ArtifactDigest || cursor == "" {
		t.Fatalf("version first page: %v, %q", versions, cursor)
	}
	upload(ownerAPI, owner.ID, "alpha", "newer")
	versions, cursor = versionsPage(ownerHTTP, endpoint+"?limit=1&cursor="+url.QueryEscape(cursor))
	if len(versions) != 1 || versions[0].Id != ordered[1].Id || versions[0].ArtifactDigest != ordered[1].ArtifactDigest || cursor != "" {
		t.Fatalf("version second page after concurrent upload: %v, %q", versions, cursor)
	}
	rows, _ = sitesPage(otherHTTP, otherPath)
	if len(rows) != 1 || rows[0].ID != isolated.SiteId {
		t.Fatalf("other project sites: %v", rows)
	}
	versions, _ = versionsPage(otherHTTP, otherPath+"/alpha/versions")
	if len(versions) != 1 || versions[0].Id != isolated.Id {
		t.Fatalf("other project versions: %v", versions)
	}
	memberRequest(t, otherHTTP, http.MethodGet, ownerPath, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, otherHTTP, http.MethodGet, endpoint, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, ownerPath+"/missing/versions", nil, http.StatusNotFound).Body.Close()
	memberRequest(t, newSessionClient(t), http.MethodGet, ownerPath, nil, http.StatusUnauthorized).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, ownerPath+"?limit=101", nil, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, ownerPath+"?cursor=UPPER", nil, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, endpoint+"?cursor=bad", nil, http.StatusBadRequest).Body.Close()
}
