//go:build integration

package database_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
)

func TestBrowserSessionRecoveryAfterServerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	runtimeDir, imageID := browserRuntimeImage(t, ctx)
	store, databaseURL, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const email = "restart-owner@cloudlet.local"
	const password = "integration-restart-password"
	owner, _ := makeProjectOwners(t, store, []string{email, "restart-other@cloudlet.local"}, password)
	projectID := uuid.MustParse(owner.ID)
	reservations, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	fixture := chromiumPage(t, ctx, docker)
	directory := t.TempDir()
	binary := filepath.Join(directory, "cloudlet-server")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, "../../cmd/server")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	instance := "browser-restart-" + uuid.NewString()[:8]
	configFile := filepath.Join(directory, "server.json")
	configJSON, err := json.Marshal(config.Config{
		Environment: "development", Instance: instance, PublicOrigin: "http://localhost",
		SiteStorageDirectory: filepath.Join(directory, "site-storage"),
		APIAddress:           "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configFile, configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	var containers []string
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, id := range containers {
			if _, err := docker.ContainerRemove(cleanupCtx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		}
	}()
	env := append(os.Environ(),
		"CLOUDLET_DATABASE_URL="+databaseURL, "CLOUDLET_SECRET_KEY="+strings.Repeat("ab", 32),
		"CLOUDLET_BROWSER_IMAGE="+imageID, "CLOUDLET_BROWSER_SECCOMP="+filepath.Join(runtimeDir, "seccomp.json"))
	start := func(lifetime string) (string, func()) {
		t.Helper()
		return startBrowserRecoveryServer(ctx, t, directory, binary, configFile, append(env[:len(env):len(env)], "CLOUDLET_BROWSER_SESSION_SECONDS="+lifetime))
	}
	base, stop := start("60")
	defer stop()
	httpClient := newSessionClient(t)
	api := authenticatedAPI(t, base, httpClient, email, password)

	create := func() (*apigen.BrowserSession, string) {
		t.Helper()
		response, err := api.CreateBrowserSessionWithResponse(ctx, owner.ID)
		if err != nil || response.StatusCode() != http.StatusCreated || response.JSON201 == nil {
			t.Fatalf("create browser session: %v (%v)", response, err)
		}
		session := response.JSON201
		reservation, err := reservations.GetReservation(ctx, projectID, session.Id)
		if err != nil || reservation.State != capacity.Active || reservation.ContainerID == "" {
			t.Fatalf("find active container reservation: %+v (%v)", reservation, err)
		}
		containers = append(containers, reservation.ContainerID)
		return session, reservation.ContainerID
	}
	reconnect := func(address string) {
		t.Helper()
		api, err = apigen.NewClientWithResponses(address, apigen.WithHTTPClient(httpClient))
		if err != nil {
			t.Fatal(err)
		}
	}
	capture := func(id uuid.UUID) []byte {
		t.Helper()
		image, err := api.CaptureBrowserScreenshotWithResponse(ctx, owner.ID, id)
		if err != nil || image.StatusCode() != http.StatusCreated || image.JSON201 == nil {
			t.Fatalf("capture original page: %v (%v)", image, err)
		}
		png, err := api.DownloadBrowserScreenshotWithResponse(ctx, owner.ID, id, image.JSON201.Id)
		if err != nil || png.StatusCode() != http.StatusOK {
			t.Fatalf("download original page: %v (%v)", png, err)
		}
		return png.Body
	}
	assertStatus := func(id uuid.UUID, wanted string) {
		t.Helper()
		saved, err := store.BrowserSession(ctx, projectID, id, instance)
		if err != nil || saved.Status != wanted {
			t.Fatalf("stored browser status = %q (%v), want %q", saved.Status, err, wanted)
		}
	}
	assertCannotNavigate := func(id uuid.UUID) {
		t.Helper()
		result, err := api.NavigateBrowserSessionWithResponse(ctx, owner.ID, id, apigen.NavigateBrowserSessionJSONRequestBody{Url: fixture})
		if err != nil || result.StatusCode() != http.StatusConflict {
			t.Fatalf("terminal session navigation: %v (%v), want HTTP 409", result, err)
		}
	}

	active, id := create()
	navigation, err := api.NavigateBrowserSessionWithResponse(ctx, owner.ID, active.Id, apigen.NavigateBrowserSessionJSONRequestBody{Url: fixture})
	if err != nil || navigation.JSON200 == nil || navigation.JSON200.Title != "Cloudlet Browser Run" {
		t.Fatalf("navigate original page: %v (%v)", navigation, err)
	}
	original := capture(active.Id)
	initial, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || initial.Container.State == nil || !initial.Container.State.Running {
		t.Fatalf("inspect original container: %v", err)
	}

	// Reproduce an interrupted startup after the reservation and runtime were
	// published but before the final ready state was recorded.
	saved, err := store.BrowserSession(ctx, projectID, active.Id, instance)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBrowserSessionStatus(ctx, saved, "starting"); err != nil {
		t.Fatal(err)
	}
	stop()
	base, stopAgain := start("60")
	defer stopAgain()
	reconnect(base)
	assertStatus(active.Id, "ready")
	after, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || after.Container.State == nil || !after.Container.State.Running || after.Container.State.StartedAt != initial.Container.State.StartedAt {
		t.Fatalf("original Docker container was not retained: %v", err)
	}
	if bound, err := reservations.GetReservation(ctx, projectID, active.Id); err != nil || bound.ContainerID != id || bound.State != capacity.Active {
		t.Fatalf("original execution binding changed: %+v (%v)", bound, err)
	}
	if continued := capture(active.Id); !bytes.Equal(original, continued) {
		t.Fatal("rendered page changed across the server restart without new navigation")
	}
	t.Log("Go process restarted; previous Docker ID, start time and rendered PNG are unchanged")

	closed, err := api.CloseBrowserSessionWithResponse(ctx, owner.ID, active.Id)
	if err != nil || closed.StatusCode() != http.StatusOK || closed.JSON200 == nil || string(closed.JSON200.Status) != "closed" {
		t.Fatalf("close recovered session: %v (%v)", closed, err)
	}
	stopAgain()
	base, stopShort := start("10")
	defer stopShort()
	reconnect(base)
	assertStatus(active.Id, "closed")
	assertCannotNavigate(active.Id)
	expiring, expiredID := create()
	stopShort()
	until := time.Now().Add(20 * time.Second)
	for {
		if _, err := docker.ContainerInspect(ctx, expiredID, client.ContainerInspectOptions{}); cerrdefs.IsNotFound(err) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if time.Now().After(until) {
			t.Fatal("container did not exit at its deadline while Go was stopped")
		}
		time.Sleep(100 * time.Millisecond)
	}
	base, stopFinal := start("60")
	defer stopFinal()
	reconnect(base)
	assertStatus(expiring.Id, "closed")
	assertCannotNavigate(expiring.Id)
	if bound, err := reservations.GetReservation(ctx, projectID, expiring.Id); err != nil || bound.State != capacity.Released {
		t.Fatalf("expired execution retained capacity: %+v (%v)", bound, err)
	}
	t.Log("closed session was rejected after restart; a session that expired while Go was offline was closed and released at startup")
}

func startBrowserRecoveryServer(ctx context.Context, t *testing.T, directory, binary, configFile string, env []string) (string, func()) {
	t.Helper()
	logFile, err := os.CreateTemp(directory, "recovery-server-*.log")
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	process := exec.CommandContext(ctx, binary, "-config", configFile)
	process.Env = env
	process.Stdout, process.Stderr = logFile, logFile
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- process.Wait()
		close(done)
	}()
	stop := sync.OnceFunc(func() {
		_ = process.Process.Signal(os.Interrupt)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(15 * time.Second):
			_ = process.Process.Kill()
			<-done
			t.Error("management process failed to stop")
		}
	})
	t.Cleanup(stop)
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("management process exited before readiness: %v\n%s", err, logs)
		case <-deadline.C:
			logs, _ := os.ReadFile(logFile.Name())
			t.Fatalf("management process did not start\n%s", logs)
		case <-ticker.C:
			logs, err := os.ReadFile(logFile.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(logs, []byte{'\n'}) {
				var entry struct {
					Message string `json:"msg"`
					API     string `json:"api_address"`
				}
				if json.Unmarshal(line, &entry) == nil && entry.Message == "cloudlet started" {
					return "http://" + entry.API, stop
				}
			}
		}
	}
}
