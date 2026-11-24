//go:build integration

package routes_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
)

func TestProjectRouteSnapshots(t *testing.T) {
	ctx := context.Background()
	store, owner, _ := openRouteStore(t, ctx)
	upstreamA := upstream(t, "A")
	upstreamB := upstream(t, "B")
	upstreamC := upstream(t, "C")
	projectA := createRouteProject(t, ctx, store, owner.ID, "项目甲")
	projectB := createRouteProject(t, ctx, store, owner.ID, "项目乙")

	handler, err := gateway.NewHandler(config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(handler)
	t.Cleanup(front.Close)
	publisher, err := routes.NewPublisher(store, handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	publishRoute(t, ctx, publisher, projectA.ID, "worker-a", "a.localhost", upstreamA.URL)
	publishRoute(t, ctx, publisher, projectB.ID, "worker-b", "b.localhost", upstreamB.URL)
	assertRoute(t, front.URL, "a.localhost", http.StatusOK, "A")
	assertRoute(t, front.URL, "b.localhost", http.StatusOK, "B")

	t.Run("foreign host path preserves routes and version", func(t *testing.T) {
		versionBefore, _, err := store.RouteSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, prefix := range []string{"/api", "/unrelated"} {
			if _, err := publisher.PublishProject(ctx, projectB.ID, []routes.Route{{
				Resource: "worker-b", Host: "A.LOCALHOST.", PathPrefix: prefix, Target: upstreamB.URL,
			}}); !errors.Is(err, database.ErrRouteConflict) {
				t.Fatalf("foreign host %s error = %v, want route conflict", prefix, err)
			}
			assertRoutePath(t, front.URL, "a.localhost", prefix+"/login", http.StatusOK, "A")
			assertRoute(t, front.URL, "b.localhost", http.StatusOK, "B")
		}
		versionAfter, _, err := store.RouteSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if versionBefore != versionAfter {
			t.Fatalf("rejected publish changed version from %d to %d", versionBefore, versionAfter)
		}
	})

	if _, err := publisher.RemoveProject(ctx, projectA.ID); err != nil {
		t.Fatal(err)
	}
	assertRoute(t, front.URL, "a.localhost", http.StatusNotFound, "404 page not found\n")
	assertRoute(t, front.URL, "b.localhost", http.StatusOK, "B")

	publishRoute(t, ctx, publisher, projectA.ID, "worker-a", "a.localhost", upstreamA.URL)
	_, err = publisher.PublishProject(ctx, projectA.ID, []routes.Route{{
		Resource: "worker-a",
		Host:     "b.localhost",
		Target:   upstreamA.URL,
	}})
	if !errors.Is(err, database.ErrRouteConflict) {
		t.Fatalf("conflicting publish error = %v, want route conflict", err)
	}
	assertRoute(t, front.URL, "a.localhost", http.StatusOK, "A")
	assertRoute(t, front.URL, "b.localhost", http.StatusOK, "B")

	staticHandler, err := gateway.NewHandler(config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: "static.localhost", Target: upstreamA.URL}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	staticPublisher, err := routes.NewPublisher(store, staticHandler)
	if err != nil {
		t.Fatal(err)
	}
	versionBefore, _, err := store.RouteSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staticPublisher.PublishProject(ctx, projectA.ID, []routes.Route{{
		Resource: "worker-a",
		Host:     "static.localhost",
		Target:   upstreamA.URL,
	}}); err == nil {
		t.Fatal("startup route host was accepted as a dynamic route")
	}
	versionAfter, _, err := store.RouteSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if versionAfter != versionBefore {
		t.Fatalf("rejected publish advanced snapshot version from %d to %d", versionBefore, versionAfter)
	}

	restartedHandler, err := gateway.NewHandler(config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	restartedFront := httptest.NewServer(restartedHandler)
	t.Cleanup(restartedFront.Close)
	restartedPublisher, err := routes.NewPublisher(store, restartedHandler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restartedPublisher.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	assertRoute(t, restartedFront.URL, "a.localhost", http.StatusOK, "A")
	assertRoute(t, restartedFront.URL, "b.localhost", http.StatusOK, "B")

	const concurrent = 6
	projects := make([]database.Project, 0, concurrent)
	for index := range concurrent {
		projects = append(projects, createRouteProject(t, ctx, store, owner.ID, fmt.Sprintf("并发项目 %d", index)))
	}
	errorsChannel := make(chan error, concurrent)
	var group sync.WaitGroup
	for index, project := range projects {
		group.Add(1)
		go func(index int, project database.Project) {
			defer group.Done()
			host := fmt.Sprintf("concurrent-%d.localhost", index)
			if _, err := restartedPublisher.PublishProject(ctx, project.ID, []routes.Route{{
				Resource: fmt.Sprintf("worker-%d", index),
				Host:     host,
				Target:   upstreamC.URL,
			}}); err != nil {
				errorsChannel <- err
			}
		}(index, project)
	}
	group.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	for index := range concurrent {
		assertRoute(t, restartedFront.URL, fmt.Sprintf("concurrent-%d.localhost", index), http.StatusOK, "C")
	}

	t.Run("concurrent host ownership", func(t *testing.T) {
		results := make(chan error, 2)
		start := make(chan struct{})
		for index, project := range []database.Project{projectA, projectB} {
			go func(index int, project database.Project) {
				<-start
				_, err := store.ReplaceProjectRoutes(ctx, project.ID, []database.ProjectRoute{{
					Resource: "worker", Host: "shared.localhost", PathPrefix: fmt.Sprintf("/project-%d", index), Target: upstreamC.URL,
				}})
				results <- err
			}(index, project)
		}
		close(start)
		successes := 0
		for range 2 {
			err := <-results
			if err == nil {
				successes++
			} else if !errors.Is(err, database.ErrRouteConflict) {
				t.Fatal(err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful host claims = %d, want 1", successes)
		}
		if _, err := publisher.RemoveProject(ctx, projectA.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.RemoveProject(ctx, projectB.ID); err != nil {
			t.Fatal(err)
		}
		publishRoute(t, ctx, publisher, projectA.ID, "worker-a", "shared.localhost", upstreamA.URL)
		assertRoute(t, front.URL, "shared.localhost", http.StatusOK, "A")
	})
}

func upstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestProjectRoutesRestoreAfterServerProcessRestart(t *testing.T) {
	ctx := t.Context()
	store, owner, connection := openRouteStore(t, ctx)
	a := upstream(t, "A")
	b := upstream(t, "B")
	projectA := createRouteProject(t, ctx, store, owner.ID, "Restart A")
	projectB := createRouteProject(t, ctx, store, owner.ID, "Restart B")
	for index, project := range []database.Project{projectA, projectB} {
		if _, err := store.ReplaceProjectRoutes(ctx, project.ID, []database.ProjectRoute{{
			Resource: "worker", Host: []string{"a.localhost", "b.localhost"}[index],
			PathPrefix: "/", Target: []string{a.URL, b.URL}[index],
		}}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "cloudlet-server")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "./cmd/server")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	data, err := json.Marshal(config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 3 {
		frontURL, stop := startRouteServer(t, binary, configPath, connection)
		if attempt < 2 {
			assertRoute(t, frontURL, "a.localhost", http.StatusOK, "A")
		} else {
			assertRoute(t, frontURL, "a.localhost", http.StatusNotFound, "404 page not found\n")
		}
		assertRoute(t, frontURL, "b.localhost", http.StatusOK, "B")
		stop()
		if attempt == 1 {
			if _, err := store.ReplaceProjectRoutes(ctx, projectA.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func startRouteServer(t *testing.T, binary, configPath, connection string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "-config", configPath)
	command.Env = append(os.Environ(), "CLOUDLET_DATABASE_URL="+connection)
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel() })
	var event struct {
		Message string `json:"msg"`
		Address string `json:"gateway_address"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(stdout).Decode(&event); err != nil {
		t.Fatalf("read server startup: %v", err)
	}
	if event.Message != "cloudlet started" || event.Address == "" {
		t.Fatalf("server startup: %+v", event)
	}
	stop := func() {
		t.Helper()
		if err := command.Process.Signal(os.Interrupt); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(io.Discard, stdout); err != nil {
			t.Fatal(err)
		}
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	return "http://" + event.Address, stop
}

func openRouteStore(t *testing.T, ctx context.Context) (*database.Store, database.User, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	container, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("route-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			if err := container.Terminate(context.WithoutCancel(ctx)); err != nil {
				t.Error(err)
			}
		})
	}
	if err != nil {
		t.Fatal(err)
	}
	connection, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.SQL.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const email = "routes@cloudlet.local"
	const password = "routes-integration-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	return store, owner, connection
}

func createRouteProject(t *testing.T, ctx context.Context, store *database.Store, ownerID, name string) database.Project {
	t.Helper()
	project, err := store.CreateProject(ctx, ownerID, name)
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func publishRoute(t *testing.T, ctx context.Context, publisher *routes.Publisher, projectID, resource, host, target string) {
	t.Helper()
	if _, err := publisher.PublishProject(ctx, projectID, []routes.Route{{
		Resource: resource,
		Host:     host,
		Target:   target,
	}}); err != nil {
		t.Fatal(err)
	}
}

func assertRoute(t *testing.T, frontURL string, host string, status int, body string) {
	t.Helper()
	assertRoutePath(t, frontURL, host, "/", status, body)
}

func assertRoutePath(t *testing.T, frontURL string, host, path string, status int, body string) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, frontURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status || string(content) != body {
		t.Fatalf("%s: status=%d body=%q, want status=%d body=%q", host, response.StatusCode, content, status, body)
	}
}
