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
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/browser"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
)

func TestBrowserSessionHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
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
	fixture, count := chromiumPage(t, ctx, docker)
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
	expire := func() error {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := store.DB.WithContext(cleanupCtx).Exec(
			"UPDATE browser_sessions SET created_at = NOW() - INTERVAL '2 seconds', expires_at = NOW() - INTERVAL '1 second' WHERE id = ?", session.Id,
		).Error; err != nil {
			return err
		}
		_, err := service.Session(cleanupCtx, projectID, session.Id)
		return err
	}
	defer func() {
		if err := expire(); err != nil {
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
	get(ownerAPI, owner.ID, http.StatusOK)
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
	click := func(api *apigen.ClientWithResponses, project, selector string, timeout *int, want int) {
		t.Helper()
		result, err := api.ClickBrowserPageWithResponse(ctx, project, session.Id,
			apigen.ClickBrowserPageJSONRequestBody{Selector: selector, TimeoutMs: timeout})
		if err != nil {
			t.Fatal(err)
		}
		if result.StatusCode() != want {
			t.Fatalf("click %#v = %d %s, want %d", selector, result.StatusCode(), result.Body, want)
		}
	}
	click(otherAPI, owner.ID, "#counter", nil, http.StatusNotFound)
	click(ownerAPI, owner.ID, " ", nil, http.StatusBadRequest)
	click(ownerAPI, owner.ID, "button", nil, http.StatusUnprocessableEntity)
	for range 2 {
		click(ownerAPI, owner.ID, "#counter", nil, http.StatusNoContent)
	}
	for deadline := time.Now().Add(3 * time.Second); count.Load() < 2 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
	}
	if count.Load() != 2 {
		t.Fatalf("page counter = %d, want 2", count.Load())
	}
	short := 200
	click(ownerAPI, owner.ID, "#absent", &short, http.StatusGatewayTimeout)
	get(ownerAPI, owner.ID, http.StatusOK)
	t.Log("real Chromium clicked the counter twice; ambiguous locator HTTP 422, absent locator HTTP 504, other project HTTP 404")
	second, err := ownerAPI.CreateBrowserSessionWithResponse(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.StatusCode() != http.StatusConflict {
		t.Fatalf("capacity response = %d %s, want 409", second.StatusCode(), second.Body)
	}
	if err := expire(); err != nil {
		t.Fatal(err)
	}
	if resolved := get(ownerAPI, owner.ID, http.StatusOK).JSON200; resolved == nil || string(resolved.Status) != "closed" {
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
}

func chromiumPage(t *testing.T, ctx context.Context, docker *client.Client) (string, *atomic.Int64) {
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
	var count atomic.Int64
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method == http.MethodPost && request.URL.Path == "/count" {
				count.Add(1)
				writer.WriteHeader(http.StatusNoContent)
				return
			}
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(writer, `<!doctype html><title>Cloudlet Browser Run</title>
<button id="counter">0</button><button>Other</button>
<script>
const button = document.querySelector('#counter');
button.onclick = () => {
  button.textContent = String(Number(button.textContent) + 1);
  void fetch('/count', { method: 'POST' });
};
</script>`)
		}),
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return "http://" + listener.Addr().String() + "/index.html", &count
}
