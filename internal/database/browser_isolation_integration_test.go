//go:build integration

package database_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestBrowserCookieAndDownloadIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	runtimeDir, image := browserRuntimeImage(t, ctx)
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "integration-isolation-password"
	emails := []string{"isolation-owner@cloudlet.local", "isolation-other@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	instance := "browser-isolation-" + uuid.NewString()[:8]
	service, err := browser.NewService(ctx, store, docker, instance, image, filepath.Join(runtimeDir, "seccomp.json"),
		filepath.Join(t.TempDir(), "screenshots"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	}()
	limits, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limits.SetHostCapacity(ctx, instance, capacity.Resources{CPUMillis: 6000, MemoryBytes: 9 << 30, Instances: 3}); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{owner.ID, other.ID} {
		if _, err := limits.SetProjectQuota(ctx, uuid.MustParse(project), capacity.Resources{CPUMillis: 4000, MemoryBytes: 6 << 30, Instances: 2}); err != nil {
			t.Fatal(err)
		}
	}

	const content = "browser session isolation fixture"
	fixture, rejectedDownloads := browserIsolationPage(t, chromiumPage(t, ctx, docker), content)
	server := httptest.NewUnstartedServer(nil)
	handler, cleanup, err := controlplane.NewHandlerWithMCP(store, nil, service,
		config.Config{Instance: instance, PublicOrigin: "http://localhost"}, slog.Default(), "http://"+server.Listener.Addr().String())
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	defer cleanup()
	server.Config.Handler = handler
	server.Start()
	defer server.Close()
	ownerAPI := authenticatedAPI(t, server.URL, newSessionClient(t), emails[0], password)
	otherAPI := authenticatedAPI(t, server.URL, newSessionClient(t), emails[1], password)

	type session struct {
		id        uuid.UUID
		project   string
		api       *apigen.ClientWithResponses
		container string
	}
	var active []session
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, item := range active {
			if _, err := service.CloseSession(cleanupCtx, uuid.MustParse(item.project), item.id); err != nil {
				t.Error(err)
			}
		}
	}()
	create := func(api *apigen.ClientWithResponses, project string) session {
		t.Helper()
		result, err := api.CreateBrowserSessionWithResponse(ctx, project)
		if err != nil || result.StatusCode() != http.StatusCreated || result.JSON201 == nil {
			t.Fatalf("create session: %v (%v)", result, err)
		}
		item := session{id: result.JSON201.Id, project: project, api: api}
		active = append(active, item)
		reservation, err := limits.GetReservation(ctx, uuid.MustParse(project), item.id)
		if err != nil || reservation.State != capacity.Active || reservation.ContainerID == "" {
			t.Fatalf("session has no active container: %+v (%v)", reservation, err)
		}
		item.container = reservation.ContainerID
		return item
	}
	navigate := func(item session, path, title string) {
		t.Helper()
		result, err := item.api.NavigateBrowserSessionWithResponse(ctx, item.project, item.id,
			apigen.NavigateBrowserSessionJSONRequestBody{Url: fixture + path})
		if err != nil || result.StatusCode() != http.StatusOK || result.JSON200 == nil || result.JSON200.Title != title {
			t.Fatalf("navigate %s %q: %v (%v), want %q", item.id, path, result, err, title)
		}
	}
	capture := func(item session, want int) *apigen.BrowserDownload {
		t.Helper()
		timeout := 1500
		result, err := item.api.CaptureBrowserDownloadWithResponse(ctx, item.project, item.id,
			apigen.CaptureBrowserDownloadJSONRequestBody{Selector: "#download", TimeoutMs: &timeout})
		if err != nil || result.StatusCode() != want {
			t.Fatalf("capture %s: %v (%v), want HTTP %d", item.id, result, err, want)
		}
		return result.JSON201
	}
	read := func(api *apigen.ClientWithResponses, project string, sessionID, downloadID uuid.UUID, want int) {
		t.Helper()
		result, err := api.GetBrowserDownloadWithResponse(ctx, project, sessionID, downloadID)
		if err != nil || result.StatusCode() != want {
			t.Fatalf("read %s: %v (%v), want HTTP %d", sessionID, result, err, want)
		}
		if want == http.StatusOK && string(result.Body) != content {
			t.Fatalf("download differs from fixture: %q", result.Body)
		}
	}
	closeSession := func(item session) {
		t.Helper()
		result, err := item.api.CloseBrowserSessionWithResponse(ctx, item.project, item.id)
		if err != nil || result.StatusCode() != http.StatusOK || result.JSON200 == nil || result.JSON200.Status != apigen.BrowserSessionStatusClosed {
			t.Fatalf("close %s: %v (%v)", item.id, result, err)
		}
		if _, err := docker.ContainerInspect(ctx, item.container, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Fatalf("closed container remains: %v", err)
		}
		reservation, err := limits.GetReservation(ctx, uuid.MustParse(item.project), item.id)
		if err != nil || reservation.State != capacity.Released {
			t.Fatalf("reservation still active: %+v (%v)", reservation, err)
		}
	}

	first := create(ownerAPI, owner.ID)
	navigate(first, "/login", "Signed in")
	navigate(first, "/", "Signed in")
	download := capture(first, http.StatusCreated)
	if download == nil || download.ArtifactDigest != fmt.Sprintf("%x", sha256.Sum256([]byte(content))) {
		t.Fatalf("unexpected download: %+v", download)
	}

	second := create(ownerAPI, owner.ID)
	outsider := create(otherAPI, other.ID)
	if first.container == second.container || first.container == outsider.container || second.container == outsider.container {
		t.Fatal("browser sessions share a container")
	}
	for _, guest := range []session{second, outsider} {
		navigate(guest, "/", "Guest")
		capture(guest, http.StatusUnprocessableEntity)
		read(guest.api, guest.project, guest.id, download.Id, http.StatusNotFound)
	}
	if rejected := rejectedDownloads.Load(); rejected != 2 {
		t.Fatalf("fixture rejected %d guest downloads, want 2", rejected)
	}
	var records int64
	if err := store.DB.WithContext(ctx).Model(&database.BrowserDownload{}).Count(&records).Error; err != nil || records != 1 {
		t.Fatalf("expected only the signed-in session's download record: %d (%v)", records, err)
	}
	read(otherAPI, owner.ID, first.id, download.Id, http.StatusNotFound)
	read(otherAPI, other.ID, first.id, download.Id, http.StatusNotFound)
	navigate(first, "/", "Signed in")
	read(ownerAPI, owner.ID, first.id, download.Id, http.StatusOK)
	t.Log("two same-project sessions and one other-project session used different containers; only the first retained the test-site Cookie and downloaded the file; both guests received 422 and left no download record")

	closeSession(first)
	read(ownerAPI, owner.ID, first.id, download.Id, http.StatusOK)
	navigate(second, "/", "Guest")
	navigate(outsider, "/", "Guest")
	closeSession(second)
	closeSession(outsider)
	for _, project := range []string{owner.ID, other.ID} {
		usage, err := limits.Usage(ctx, instance, uuid.MustParse(project))
		if err != nil || usage.HostUsed != (capacity.Resources{}) || usage.ProjectUsed != (capacity.Resources{}) {
			t.Fatalf("cleanup did not release capacity: %+v (%v)", usage, err)
		}
	}
	t.Log("closing the first session left the other sessions usable; all three containers and reservations were released, while the authorized artifact remained readable")
}

func browserIsolationPage(t *testing.T, fixture, content string) (string, *atomic.Int64) {
	t.Helper()
	gateway, err := url.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(gateway.Hostname(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	var rejectedDownloads atomic.Int64
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("cloudlet_fixture_login")
		signedIn := err == nil && cookie.Value == "fixture"
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "cloudlet_fixture_login", Value: "fixture", Path: "/", HttpOnly: true})
			http.Redirect(w, r, "/", http.StatusSeeOther)
		case "/file":
			if !signedIn {
				rejectedDownloads.Add(1)
				http.Error(w, "login required", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Disposition", `attachment; filename="isolation.txt"`)
			w.Header().Set("Content-Type", "application/octet-stream")
			fmt.Fprint(w, content)
		case "/":
			title := "Guest"
			if signedIn {
				title = "Signed in"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><title>%s</title><a id="download" href="/file" download>File</a>`, title)
		default:
			http.NotFound(w, r)
		}
	})}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String(), &rejectedDownloads
}
