//go:build integration

package database_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
)

func TestBrowserSessionCleanupHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	runtimeDir, imageID := browserRuntimeImage(t, ctx)
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const password = "integration-cleanup-password"
	emails := []string{"cleanup-owner@cloudlet.local", "cleanup-other@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	projectID := uuid.MustParse(owner.ID)
	docker, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	const instance = "browser-cleanup-integration"
	const lifetime = 15 * time.Second
	service, err := browser.NewService(ctx, store, docker, instance, imageID, filepath.Join(runtimeDir, "seccomp.json"), t.TempDir(), make([]byte, 32))
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
	reaperCtx, stopReaper := context.WithCancel(ctx)
	reaperDone := make(chan struct{})
	go func() {
		defer close(reaperDone)
		service.RunReaper(reaperCtx, slog.Default())
	}()
	defer func() {
		stopReaper()
		<-reaperDone
	}()

	server := httptest.NewUnstartedServer(nil)
	handler, cleanup, err := controlplane.NewHandlerWithMCP(store, nil, service,
		config.Config{Instance: instance, PublicOrigin: "http://localhost", BrowserSessionLifetime: lifetime},
		slog.Default(), "http://"+server.Listener.Addr().String())
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

	type activeSession struct {
		session   apigen.BrowserSession
		container string
	}
	var active []uuid.UUID
	defer func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, execution := range active {
			if _, err := service.CloseSession(cleanupCtx, projectID, execution); err != nil {
				t.Error(err)
			}
		}
	}()
	create := func() activeSession {
		t.Helper()
		response, err := ownerAPI.CreateBrowserSessionWithResponse(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != http.StatusCreated || response.JSON201 == nil || string(response.JSON201.Status) != "ready" {
			t.Fatalf("create browser session = %d %s", response.StatusCode(), response.Body)
		}
		execution := activeSession{session: *response.JSON201}
		active = append(active, execution.session.Id)
		reservation, err := limits.GetReservation(ctx, projectID, execution.session.Id)
		if err != nil || reservation.State != capacity.Active || reservation.ContainerID == "" {
			t.Fatalf("new session has no active container reservation: %+v (%v)", reservation, err)
		}
		container, err := docker.ContainerInspect(ctx, reservation.ContainerID, client.ContainerInspectOptions{})
		if err != nil || container.Container.State == nil || !container.Container.State.Running {
			t.Fatalf("browser container is not running: %v", err)
		}
		execution.container = reservation.ContainerID
		return execution
	}
	closeSession := func(api *apigen.ClientWithResponses, project string, id uuid.UUID, want int) {
		t.Helper()
		response, err := api.CloseBrowserSessionWithResponse(ctx, project, id)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode() != want {
			t.Fatalf("close = %d %s, want %d", response.StatusCode(), response.Body, want)
		}
		if want == http.StatusOK && (response.JSON200 == nil || string(response.JSON200.Status) != "closed") {
			t.Fatalf("close response has no closed state: %s", response.Body)
		}
	}
	assertCleanup := func(execution activeSession) capacity.Reservation {
		t.Helper()
		if _, err := docker.ContainerInspect(ctx, execution.container, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Fatalf("inspect after cleanup: %v, want container not found", err)
		}
		reservation, err := limits.GetReservation(ctx, projectID, execution.session.Id)
		if err != nil || reservation.State != capacity.Released || reservation.ReleasedAt == nil {
			t.Fatalf("cleanup did not release reservation: %+v (%v)", reservation, err)
		}
		usage, err := limits.Usage(ctx, instance, projectID)
		if err != nil || usage.HostUsed != (capacity.Resources{}) || usage.ProjectUsed != (capacity.Resources{}) {
			t.Fatalf("cleanup did not release resource quota: %+v (%v)", usage, err)
		}
		return reservation
	}

	manual := create()
	closeSession(otherAPI, owner.ID, manual.session.Id, http.StatusNotFound)
	closeSession(otherAPI, other.ID, manual.session.Id, http.StatusNotFound)
	if _, err := docker.ContainerInspect(ctx, manual.container, client.ContainerInspectOptions{}); err != nil {
		t.Fatalf("unauthorized requests removed the container: %v", err)
	}
	closeSession(ownerAPI, owner.ID, manual.session.Id, http.StatusOK)
	closeSession(ownerAPI, owner.ID, manual.session.Id, http.StatusOK)
	assertCleanup(manual)
	t.Log("manual close and repeated close HTTP 200, status=closed; cross-project close HTTP 404; Docker container removed and quota released")

	expiring := create()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		// Read the database directly. A GET would also clean up an expired
		// session and would hide a failure in the background reaper.
		session, err := store.BrowserSession(ctx, projectID, expiring.session.Id, instance)
		if err != nil {
			t.Fatal(err)
		}
		if session.Status == "closed" {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("background expiration did not close the session")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-poll.C:
		}
	}
	reservation := assertCleanup(expiring)
	if reservation.ReleasedAt.Before(expiring.session.ExpiresAt) {
		t.Fatal("capacity was released before the session deadline")
	}
	ended, err := ownerAPI.GetBrowserSessionWithResponse(ctx, owner.ID, expiring.session.Id)
	if err != nil || ended.StatusCode() != http.StatusOK || ended.JSON200 == nil || string(ended.JSON200.Status) != "closed" {
		t.Fatalf("expired session has no closed state via management API: %v (%v)", ended, err)
	}
	t.Log("15-second session expired without an API request; Docker container removed, status=closed and quota released")
}
