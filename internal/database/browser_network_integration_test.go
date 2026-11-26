//go:build integration

package database_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
)

func TestBrowserRunManagementNetworkIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	runtimeDir, image := browserRuntimeImage(t, ctx)
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const email, password = "network-owner@cloudlet.local", "network-integration-password"
	owner, _ := makeProjectOwners(t, store, []string{email, "network-other@cloudlet.local"}, password)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	instance := "browser-network-" + uuid.NewString()[:8]
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

	reservations, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	publicPage := chromiumPage(t, ctx, docker)
	publicURL, err := url.Parse(publicPage)
	if err != nil {
		t.Fatal(err)
	}
	const marker = "Cloudlet private network fixture"
	network := instance + "-management"
	managementAddress := managementNetworkFixture(t, ctx, image, network, marker)
	hostOnly := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, marker)
	}))
	defer hostOnly.Close()
	hostURL, err := url.Parse(hostOnly.URL)
	if err != nil {
		t.Fatal(err)
	}
	response, err := hostOnly.Client().Get(hostOnly.URL)
	if err != nil {
		t.Fatal(err)
	}
	content, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || readErr != nil || string(content) != marker {
		t.Fatalf("localhost fixture not available: HTTP %d, %v", response.StatusCode, readErr)
	}
	hostOnlyAddress := "http://" + net.JoinHostPort(publicURL.Hostname(), hostURL.Port()) + "/"

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
	api := authenticatedAPI(t, server.URL, newSessionClient(t), email, password)
	projectID := uuid.MustParse(owner.ID)

	for _, destination := range []struct{ name, address string }{
		{"management bridge", managementAddress}, {"host loopback", hostOnlyAddress},
	} {
		t.Run(destination.name, func(t *testing.T) {
			created, err := api.CreateBrowserSessionWithResponse(ctx, owner.ID)
			if err != nil || created.StatusCode() != http.StatusCreated || created.JSON201 == nil {
				t.Fatalf("create isolated browser: %v (%v)", created, err)
			}
			sessionID := created.JSON201.Id
			t.Cleanup(func() {
				stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if _, err := service.CloseSession(stopCtx, projectID, sessionID); err != nil {
					t.Error(err)
				}
			})
			binding, err := reservations.GetReservation(ctx, projectID, sessionID)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := docker.ContainerInspect(ctx, binding.ContainerID, client.ContainerInspectOptions{})
			if err != nil || observed.Container.NetworkSettings == nil {
				t.Fatalf("inspect browser networking: %v", err)
			}
			networks := observed.Container.NetworkSettings.Networks
			if _, attached := networks[network]; attached {
				t.Fatalf("browser is attached to the management network: %v", networks)
			}
			if _, attached := networks["bridge"]; !attached {
				t.Fatalf("expected the published-mode browser bridge: %v", networks)
			}
			navigate := func(address string) *apigen.NavigateBrowserSessionResponse {
				t.Helper()
				result, err := api.NavigateBrowserSessionWithResponse(ctx, owner.ID, sessionID,
					apigen.NavigateBrowserSessionJSONRequestBody{Url: address})
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			opened := navigate(publicPage)
			if opened.StatusCode() != http.StatusOK || opened.JSON200 == nil || opened.JSON200.Title != "Cloudlet Browser Run" {
				t.Fatalf("public test app was not accessible: %d %s", opened.StatusCode(), opened.Body)
			}
			rejected := navigate(destination.address)
			if rejected.StatusCode() != http.StatusBadGateway && rejected.StatusCode() != http.StatusGatewayTimeout {
				t.Fatalf("private fixture returned unexpected HTTP %d: %s", rejected.StatusCode(), rejected.Body)
			}
			t.Logf("public test app HTTP 200; private %s denied with HTTP %d", destination.name, rejected.StatusCode())
		})
	}
}

func managementNetworkFixture(t *testing.T, ctx context.Context, image, network, marker string) string {
	t.Helper()
	chromeDocker := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "docker", args...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("prepare isolated management fixture: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	chromeDocker("network", "create", network)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "network", "rm", network).CombinedOutput(); err != nil {
			t.Errorf("remove management test network: %v\n%s", err, output)
		}
	})
	name := network + "-fixture"
	chromeDocker("run", "-d", "--name", name, "--network", network, "--entrypoint", "/usr/local/bin/node", image,
		"-e", `require('node:http').createServer((_,res) => res.end(process.argv[1])).listen(8080,'0.0.0.0')`, marker)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("remove management test container: %v\n%s", err, output)
		}
	})
	peer := chromeDocker("run", "--rm", "--network", network, "--entrypoint", "/usr/local/bin/node", image,
		"-e", `
const wait = require('node:timers/promises').setTimeout;
(async () => {
  for (let i = 0; i < 30; i++) {
    try {
      const res = await fetch(process.argv[1], {signal: AbortSignal.timeout(1000)});
      if (res.ok) { console.log(await res.text()); return; }
    } catch {}
    await wait(100);
  }
  process.exitCode = 1;
})();
`, "http://"+name+":8080/")
	if peer != marker {
		t.Fatalf("same-network peer could not read the dedicated fixture: %q", peer)
	}
	address := chromeDocker("inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if net.ParseIP(address) == nil {
		t.Fatalf("invalid management fixture address: %q", address)
	}
	return "http://" + net.JoinHostPort(address, "8080") + "/"
}
