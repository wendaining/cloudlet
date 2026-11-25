//go:build integration

package database_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/staticdeploy"
)

func TestStaticSiteArchiveDownloadHTTP(t *testing.T) {
	ctx := t.Context()
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "archive-download-test-password"
	const ownerEmail, otherEmail = "archive-owner@cloudlet.local", "archive-other@cloudlet.local"
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
	handler, closeSessions, err := controlplane.NewHandlerWithMCP(store, sites, nil,
		config.Config{Instance: "archive-test", PublicOrigin: "http://localhost"}, slog.Default(), "http://"+server.Listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = handler
	server.Start()
	t.Cleanup(func() { server.Close(); closeSessions() })
	ownerHTTP, otherHTTP := newSessionClient(t), newSessionClient(t)
	ownerAPI := authenticatedAPI(t, server.URL, ownerHTTP, ownerEmail, password)
	authenticatedAPI(t, server.URL, otherHTTP, otherEmail, password)
	source := siteZIP(t, "<p>original ZIP</p>", "assets/style.css")
	created, err := ownerAPI.UploadStaticSiteVersionWithBodyWithResponse(ctx, owner.ID, "sample", "application/zip", bytes.NewReader(source))
	if err != nil || created.JSON201 == nil {
		t.Fatalf("upload archive: %v, %v", created, err)
	}
	version := created.JSON201
	downloadURL := func(project, name string, id uuid.UUID) string {
		return server.URL + "/api/v1/projects/" + project + "/sites/" + name + "/versions/" + id.String() + "/artifact"
	}
	endpoint := downloadURL(owner.ID, "sample", version.Id)
	download := func(client *http.Client) {
		t.Helper()
		response := memberRequest(t, client, http.MethodGet, endpoint, nil, http.StatusOK)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || !bytes.Equal(body, source) {
			t.Fatalf("downloaded data mismatch: %d bytes, %v", len(body), err)
		}
		sum := sha256.Sum256(body)
		disposition, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
		if err != nil || disposition != "attachment" || parameters["filename"] != "sample-"+version.Id.String()+".zip" ||
			response.Header.Get("Content-Type") != "application/zip" ||
			response.Header.Get("Content-Length") != strconv.Itoa(len(source)) ||
			response.Header.Get("Content-Digest") != "sha-256=:"+base64.StdEncoding.EncodeToString(sum[:])+":" ||
			response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("download headers = %v, %v", response.Header, err)
		}
	}
	download(ownerHTTP)
	memberRequest(t, otherHTTP, http.MethodGet, endpoint, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, otherHTTP, http.MethodGet, downloadURL(other.ID, "sample", version.Id), nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, downloadURL(owner.ID, "different", version.Id), nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerHTTP, http.MethodGet, downloadURL(owner.ID, "sample", uuid.New()), nil, http.StatusNotFound).Body.Close()
	memberRequest(t, newSessionClient(t), http.MethodGet, endpoint, nil, http.StatusUnauthorized).Body.Close()

	viewer, err := store.Authenticate(ctx, otherEmail, password)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, owner.ID, viewer.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	download(otherHTTP)
}
