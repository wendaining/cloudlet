//go:build integration

package database_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestBrowserSessionHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	runtimeDir, imageID := browserRuntimeImage(t, ctx)
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "integration-browser-password"
	emails := []string{"browser-owner@cloudlet.local", "browser-other@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	const instance = "browser-integration"
	service, err := browser.NewService(ctx, store, docker, instance, imageID, filepath.Join(runtimeDir, "seccomp.json"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	fixture := chromiumPage(t, ctx, docker)
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

	start, err := ownerAPI.CreateBrowserSessionWithResponse(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if start.StatusCode() != http.StatusCreated || start.JSON201 == nil {
		t.Fatalf("create = %d %s", start.StatusCode(), start.Body)
	}
	session := start.JSON201
	projectID := uuid.MustParse(owner.ID)
	expire := func(id uuid.UUID) error {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := store.DB.WithContext(cleanupCtx).Exec(
			"UPDATE browser_sessions SET created_at = NOW() - INTERVAL '2 seconds', expires_at = NOW() - INTERVAL '1 second' WHERE id = ?", id,
		).Error; err != nil {
			return err
		}
		_, err := service.Session(cleanupCtx, projectID, id)
		return err
	}
	defer func() {
		if err := expire(session.Id); err != nil {
			t.Error(err)
		}
	}()
	endpoint := "/api/v1/projects/" + owner.ID + "/browser-sessions/" + session.Id.String()
	if session.Endpoint != endpoint || start.HTTPResponse.Header.Get("Location") != endpoint || string(session.Status) != "ready" {
		t.Fatalf("unexpected created session: %+v", session)
	}
	t.Logf("create HTTP %d, status=%s, endpoint=%s", start.StatusCode(), session.Status, endpoint)

	get := func(api *apigen.ClientWithResponses, project string, want int) *apigen.GetBrowserSessionResponse {
		t.Helper()
		response, err := api.GetBrowserSessionWithResponse(ctx, project, session.Id)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != want {
			t.Fatalf("get = %d %s, want %d", response.StatusCode(), response.Body, want)
		}
		return response
	}
	if detail := get(ownerAPI, owner.ID, http.StatusOK).JSON200; detail == nil ||
		detail.DesiredStatus != apigen.BrowserSessionStatusReady || detail.ActualStatus != apigen.BrowserSessionStatusReady || detail.LastError != nil {
		t.Fatalf("unexpected running session view: %+v", detail)
	}
	get(otherAPI, owner.ID, http.StatusNotFound)
	get(otherAPI, other.ID, http.StatusNotFound)

	navigate := func(api *apigen.ClientWithResponses, project, target string, want int) *apigen.NavigateBrowserSessionResponse {
		t.Helper()
		response, err := api.NavigateBrowserSessionWithResponse(ctx, project, session.Id, apigen.NavigateBrowserSessionJSONRequestBody{Url: target})
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != want {
			t.Fatalf("navigate = %d %s, want %d", response.StatusCode(), response.Body, want)
		}
		return response
	}
	navigate(otherAPI, owner.ID, fixture, http.StatusNotFound)
	navigate(ownerAPI, owner.ID, "file:///not-a-web-page", http.StatusBadRequest)
	page := navigate(ownerAPI, owner.ID, fixture, http.StatusOK).JSON200
	if page == nil || page.Url != fixture || page.Title != "Cloudlet Browser Run" {
		t.Fatalf("unexpected Chromium page: %+v", page)
	}
	t.Logf("navigation HTTP 200, title=%q, url=%s; cross-project read and navigation HTTP 404", page.Title, page.Url)
	readConsole := func(api *apigen.ClientWithResponses, project string, id uuid.UUID, want int) *apigen.BrowserConsole {
		t.Helper()
		result, err := api.GetBrowserConsoleWithResponse(ctx, project, id)
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode() != want {
			t.Fatalf("read console = %d %s, want %d", result.StatusCode(), result.Body, want)
		}
		return result.JSON200
	}
	readConsole(otherAPI, owner.ID, session.Id, http.StatusNotFound)
	history := readConsole(ownerAPI, owner.ID, session.Id, http.StatusOK)
	if history == nil || len(history.Entries) != 100 || history.Dropped < 10 {
		t.Fatalf("console retention is not bounded: %+v", history)
	}
	var logged, truncated, threw bool
	for _, entry := range history.Entries {
		logged = logged || (entry.Kind == "console" && entry.Level == "warning" && entry.Message == "console fixture warning")
		truncated = truncated || (entry.Kind == "console" && entry.Truncated && entry.Message == strings.Repeat("x", 512))
		threw = threw || (entry.Kind == "page_error" && entry.Message == "console fixture exception")
	}
	if !logged || !truncated || !threw {
		t.Fatalf("warning, truncated message or page error missing: warn=%v truncated=%v error=%v", logged, truncated, threw)
	}
	t.Log("console retains 100 messages, truncates long entries, captures uncaught errors and rejects another project")
	second, err := ownerAPI.CreateBrowserSessionWithResponse(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.StatusCode() != http.StatusConflict {
		t.Fatalf("capacity response = %d %s, want 409", second.StatusCode(), second.Body)
	}
	list := func(api *apigen.ClientWithResponses, project string, params *apigen.ListBrowserSessionsParams, want int) *apigen.BrowserSessionPage {
		t.Helper()
		response, err := api.ListBrowserSessionsWithResponse(ctx, project, params)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != want {
			t.Fatalf("list sessions = %d %s, want %d", response.StatusCode(), response.Body, want)
		}
		return response.JSON200
	}
	invalid := 0
	list(ownerAPI, owner.ID, &apigen.ListBrowserSessionsParams{Limit: &invalid}, http.StatusBadRequest)
	list(otherAPI, owner.ID, nil, http.StatusNotFound)
	if isolated := list(otherAPI, other.ID, nil, http.StatusOK); isolated == nil || len(isolated.Items) != 0 {
		t.Fatalf("other project unexpectedly sees sessions: %+v", isolated)
	}
	one := 1
	firstPage := list(ownerAPI, owner.ID, &apigen.ListBrowserSessionsParams{Limit: &one}, http.StatusOK)
	if firstPage == nil || firstPage.NextCursor == nil || len(firstPage.Items) != 1 {
		t.Fatalf("unexpected session page: %+v", firstPage)
	}
	failed := firstPage.Items[0]
	if failed.ActualStatus != apigen.BrowserSessionStatusFailed || failed.LastError == nil || *failed.LastError != "浏览器会话容量不足" {
		t.Fatalf("quota failure not recorded: %+v", failed)
	}
	secondPage := list(ownerAPI, owner.ID, &apigen.ListBrowserSessionsParams{Limit: &one, Before: firstPage.NextCursor}, http.StatusOK)
	if secondPage == nil || secondPage.NextCursor != nil || len(secondPage.Items) != 1 || secondPage.Items[0].Id != session.Id {
		t.Fatalf("unexpected second session page: %+v", secondPage)
	}
	list(otherAPI, other.ID, &apigen.ListBrowserSessionsParams{Before: firstPage.NextCursor}, http.StatusNotFound)
	if err := expire(session.Id); err != nil {
		t.Fatal(err)
	}
	if resolved := get(ownerAPI, owner.ID, http.StatusOK).JSON200; resolved == nil ||
		resolved.ActualStatus != apigen.BrowserSessionStatusClosed || resolved.DesiredStatus != apigen.BrowserSessionStatusClosed {
		t.Fatalf("expired session is not closed: %+v", resolved)
	}
	limits, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := limits.Usage(ctx, instance, projectID)
	if err != nil || usage.HostUsed.Instances != 0 || usage.ProjectUsed.Instances != 0 {
		t.Fatalf("expired session still holds capacity: %+v (%v)", usage, err)
	}
	replacement, err := ownerAPI.CreateBrowserSessionWithResponse(ctx, owner.ID)
	if err != nil || replacement.StatusCode() != http.StatusCreated || replacement.JSON201 == nil {
		t.Fatalf("create second real session: %v (%v)", replacement, err)
	}
	defer func() {
		if err := expire(replacement.JSON201.Id); err != nil {
			t.Error(err)
		}
	}()
	pageWithHistory := list(ownerAPI, owner.ID, nil, http.StatusOK)
	if len(pageWithHistory.Items) != 3 || pageWithHistory.Items[0].Id != replacement.JSON201.Id ||
		pageWithHistory.Items[0].ActualStatus != apigen.BrowserSessionStatusReady ||
		pageWithHistory.Items[2].ActualStatus != apigen.BrowserSessionStatusClosed {
		t.Fatalf("wrong history after creating a second session: %+v", pageWithHistory)
	}
	interrupted := database.BrowserSession{
		ID: uuid.New(), ProjectID: projectID, HostID: instance, Status: "starting",
		CreatedAt: time.Now().Add(-2 * time.Minute), ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := store.CreateBrowserSession(ctx, interrupted); err != nil {
		t.Fatal(err)
	}
	interruptedPage := list(ownerAPI, owner.ID, nil, http.StatusOK)
	var found bool
	for _, item := range interruptedPage.Items {
		if item.Id != interrupted.ID {
			continue
		}
		found = true
		detail, err := ownerAPI.GetBrowserSessionWithResponse(ctx, owner.ID, interrupted.ID)
		if err != nil || detail.JSON200 == nil || item.ActualStatus != apigen.BrowserSessionStatusClosed ||
			detail.JSON200.ActualStatus != item.ActualStatus || detail.JSON200.DesiredStatus != item.DesiredStatus {
			t.Fatalf("inconsistent interrupted session: list=%+v detail=%+v error=%v", item, detail, err)
		}
	}
	if !found {
		t.Fatal("interrupted session is missing from the list")
	}
	t.Log("expired interrupted startup is closed in both list and detail")
	t.Log("project list and detail expose ready, closed and failed state; bounded pagination and cross-project scope verified")
	isolated := readConsole(ownerAPI, owner.ID, replacement.JSON201.Id, http.StatusOK)
	if isolated == nil || len(isolated.Entries) != 0 || isolated.Dropped != 0 {
		t.Fatalf("previous session console leaked into new session: %+v", isolated)
	}
	t.Log("a second real Chromium session has no logs from the first")
}

func browserRuntimeImage(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	runtimeDir, err := filepath.Abs("../../runtimes/browser")
	if err != nil {
		t.Fatal(err)
	}
	imageID := os.Getenv("CLOUDLET_BROWSER_TEST_IMAGE")
	if imageID == "" {
		output, err := exec.CommandContext(ctx, "docker", "build", "--quiet", runtimeDir).CombinedOutput()
		if err != nil {
			t.Fatalf("build Chromium runtime: %v\n%s", err, output)
		}
		imageID = strings.TrimSpace(string(output))
	}
	return runtimeDir, imageID
}

func chromiumPage(t *testing.T, ctx context.Context, docker *client.Client) string {
	t.Helper()
	bridge, err := docker.NetworkInspect(ctx, "bridge", client.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var address string
	for _, subnet := range bridge.Network.IPAM.Config {
		if subnet.Gateway.Is4() {
			address = subnet.Gateway.String()
			break
		}
	}
	if address == "" {
		t.Fatal("Docker bridge has no IPv4 gateway")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(writer, `<!doctype html><title>Cloudlet Browser Run</title>
<script>
for (let i = 0; i < 110; i++) console.log('entry ' + i);
console.warn('console fixture warning');
console.log('x'.repeat(600));
throw new Error('console fixture exception');
</script>`)
		}),
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String() + "/index.html"
}
