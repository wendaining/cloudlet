//go:build integration

package containerpublish_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containerpublish"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
)

// TestContainerTakeoverAfterControllerRestart creates real containers, stops the
// controller and starts it again. The same Docker containers must be adopted,
// their actual endpoints and routes restored, and no duplicate execution may be
// created. A container stopped outside the platform is started once; a container
// removed outside the platform is recorded as failed without being recreated.
func TestContainerTakeoverAfterControllerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	images := readContainerImages(t)
	platform, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("takeover-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, platform)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := platform.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.SQL.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := docker.Close(); err != nil {
			t.Error(err)
		}
	})

	instance := "takeover-" + uuid.NewString()[:8]
	configuration := config.ContainerRuntime{
		HostID:       "takeover-host-" + uuid.NewString()[:8],
		HostBudget:   config.ResourceBudget{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
		ProjectQuota: config.ResourceBudget{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
	}
	secretKey := make([]byte, 32)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		cleanContainers(t, cleanupCtx, docker, instance)
	})

	const email = "takeover-owner@cloudlet.local"
	const password = "takeover-integration-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	owner, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, owner.ID, "Container takeover")
	if err != nil {
		t.Fatal(err)
	}
	projectID := uuid.MustParse(project.ID)

	// A real business database backs the bound container. The application
	// inserts one row per start, so a takeover that accidentally starts a new
	// container changes the response from "1" to "2".
	business, businessHost, ca := businessPostgres(t, ctx, images.Postgres)
	bindingName := "business-db"
	credentials, err := json.Marshal(map[string]string{"username": businessUser, "password": businessPassword})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := store.CreateProjectSecretVersion(ctx, project.ID, "APP_DB", string(credentials), secretKey)
	if err != nil {
		t.Fatal(err)
	}
	found, err := store.CreatePostgresBinding(ctx, database.PostgresBinding{
		ProjectID: project.ID, Name: bindingName, Host: businessHost, Port: 5432,
		Database: businessDatabase, SecretVersionID: uuid.MustParse(secret.ID), VerifiedAt: time.Now(),
	})
	if err != nil || !found {
		t.Fatalf("seed business database binding: %t %v", found, err)
	}

	start := func() (*containerpublish.Service, string, func()) {
		t.Helper()
		front, err := gateway.NewHandler(config.Config{
			APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		publisher, err := routes.NewPublisher(store, front)
		if err != nil {
			t.Fatal(err)
		}
		service, err := containerpublish.NewService(ctx, store, docker, publisher, configuration, instance, secretKey)
		if err != nil {
			t.Fatal(err)
		}
		public := httptest.NewServer(front)
		var once sync.Once
		stop := func() {
			once.Do(func() {
				public.Close()
				if err := service.Close(); err != nil {
					t.Error(err)
				}
			})
		}
		return service, public.URL, stop
	}

	service, publicURL, stop := start()
	defer func() { stop() }()

	web, _, err := service.Create(ctx, projectID, "takeover-web", containerpublish.Request{
		Name: "web", Image: images.ContainerHTTP, Port: 8080, Hostname: "takeover.localhost",
		CPUMillis: 500, MemoryBytes: 128 << 20, User: "101:101",
		DataVolume: &containerpublish.DataVolumeRequest{MountPath: "/data", Directory: "state"},
	})
	if err != nil {
		t.Fatal(err)
	}
	app, _, err := service.Create(ctx, projectID, "takeover-app", containerpublish.Request{
		Name: "bound", Image: images.Postgres, Port: 8080, Hostname: "takeover-bound.localhost",
		CPUMillis: 500, MemoryBytes: 128 << 20,
		Command: []string{"/bin/sh", "-ec", businessAppCommand},
		Env:     []string{"FIXTURE_CA=" + ca}, PostgresBinding: &bindingName,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeContainerFile(t, ctx, web.Execution.DockerContainerID, "printf cloudlet-takeover > /data/state/note.txt")

	publicClient := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(publicClient.CloseIdleConnections)
	getPublic := func(base, host string) (int, string) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = host
		response, err := publicClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}
	assertWeb := func(base string) {
		t.Helper()
		status, body := getPublic(base, "takeover.localhost")
		if status != http.StatusOK || !strings.Contains(strings.ToLower(body), "nginx") {
			t.Fatalf("web container: HTTP %d %q", status, body)
		}
	}
	assertBound := func(base string) {
		t.Helper()
		status, body := getPublic(base, "takeover-bound.localhost")
		if status != http.StatusOK || body != "1" {
			t.Fatalf("bound container: HTTP %d %q, want 1", status, body)
		}
		assertBusinessRows(t, ctx, business, 1)
	}
	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	assertCapacity := func(instances int64) {
		t.Helper()
		usage, err := capacityStore.ProjectUsage(ctx, projectID)
		if err != nil {
			t.Fatal(err)
		}
		want := capacity.Resources{CPUMillis: 500 * instances, MemoryBytes: (128 << 20) * instances, Instances: instances}
		if usage.Used != want {
			t.Fatalf("project capacity = %+v, want %+v", usage.Used, want)
		}
	}
	assertWeb(publicURL)
	assertBound(publicURL)
	assertCapacity(2)
	webStart := containerStartedAt(t, ctx, docker, web.Execution.DockerContainerID)
	appStart := containerStartedAt(t, ctx, docker, app.Execution.DockerContainerID)

	// Restart the controller with a fresh Gateway. Corrupt the recorded route
	// targets first so takeover must read the actual Docker endpoints instead
	// of trusting the persisted values.
	stop()
	// Remove the persisted route so takeover must restore it from the actual
	// Docker endpoint.
	if _, err := store.SQL.ExecContext(ctx, "DELETE FROM project_routes WHERE project_id = $1 AND resource = $2", project.ID, web.Container.ID.String()); err != nil {
		t.Fatal(err)
	}
	service, publicURL, stop = start()
	assertWeb(publicURL)
	assertBound(publicURL)
	assertCapacity(2)
	assertSameExecution(t, ctx, service, projectID, web)
	assertSameExecution(t, ctx, service, projectID, app)
	if got := containerStartedAt(t, ctx, docker, web.Execution.DockerContainerID); got != webStart {
		t.Fatalf("web container start time changed from %q to %q", webStart, got)
	}
	if got := containerStartedAt(t, ctx, docker, app.Execution.DockerContainerID); got != appStart {
		t.Fatalf("bound container start time changed from %q to %q", appStart, got)
	}
	assertContainerCount(t, ctx, docker, web.Container.ID, 1)
	assertContainerCount(t, ctx, docker, app.Container.ID, 1)
	assertRouteTarget(t, ctx, store, web.Container.ID, web.Execution.ID)
	writeContainerFile(t, ctx, web.Execution.DockerContainerID, "cat /data/state/note.txt")

	// Another instance must never adopt or fail this controller's containers.
	// The controller lock is per host, so stop this controller first and keep
	// the same host identifier for the other environment.
	stop()
	otherInstance := instance + "-other"
	otherFront, err := gateway.NewHandler(config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	otherPublisher, err := routes.NewPublisher(store, otherFront)
	if err != nil {
		t.Fatal(err)
	}
	other, err := containerpublish.NewService(ctx, store, docker, otherPublisher, configuration, otherInstance, secretKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	assertContainerCount(t, ctx, docker, web.Container.ID, 1)
	if container := containerState(t, ctx, store, web.Container.ID); container != "active" {
		t.Fatalf("another instance changed the container state to %q", container)
	}

	// A container stopped outside the platform is started once and its route is
	// restored. The execution identifier and data volume stay the same.
	stop()
	seconds := 10
	if _, err := docker.ContainerStop(ctx, web.Execution.DockerContainerID, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
		t.Fatal(err)
	}
	// Corrupt both recorded targets while the container is stopped. Starting
	// it again must refresh the execution and route from Docker.
	if _, err := store.SQL.ExecContext(ctx, "UPDATE container_executions SET target = 'http://127.0.0.1:1' WHERE id = $1", web.Execution.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQL.ExecContext(ctx, "UPDATE project_routes SET target = 'http://127.0.0.1:1' WHERE project_id = $1 AND resource = $2", project.ID, web.Container.ID.String()); err != nil {
		t.Fatal(err)
	}
	service, publicURL, stop = start()
	assertWeb(publicURL)
	assertBound(publicURL)
	assertSameExecution(t, ctx, service, projectID, web)
	assertCapacity(2)
	if got := containerStartedAt(t, ctx, docker, web.Execution.DockerContainerID); got == webStart {
		t.Fatal("stopped container was not started again")
	}
	assertContainerCount(t, ctx, docker, web.Container.ID, 1)
	if got := readContainerFile(t, ctx, web.Execution.DockerContainerID, "cat /data/state/note.txt"); got != "cloudlet-takeover" {
		t.Fatalf("persisted volume content = %q", got)
	}

	// A recorded container that no longer exists is not recreated. Its route is
	// revoked, capacity released and the container recorded as failed.
	stop()
	if _, err := docker.ContainerRemove(ctx, web.Execution.DockerContainerID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	service, publicURL, stop = start()
	assertBound(publicURL)
	assertCapacity(1)
	if status, _ := getPublic(publicURL, "takeover.localhost"); status != http.StatusNotFound {
		t.Fatalf("missing container public status = %d", status)
	}
	assertContainerCount(t, ctx, docker, web.Container.ID, 0)
	if container := containerState(t, ctx, store, web.Container.ID); container != "failed" {
		t.Fatalf("missing container state = %q, want failed", container)
	}
	if reservation, err := capacityStore.GetReservation(ctx, projectID, web.Execution.ID); err != nil || reservation.State != capacity.Released {
		t.Fatalf("missing container reservation = %+v (%v), want released", reservation, err)
	}
	if volume := web.Execution.VolumeName; volume == "" || !dockerVolumeExists(t, ctx, docker, volume) {
		t.Fatalf("missing container did not preserve data volume %q", volume)
	}

	// The failed container can be restarted from its saved configuration and
	// volume without any leftover Docker container.
	restarted, err := service.Restart(ctx, projectID, web.Container.ID, "takeover-restart")
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Execution.ID == web.Execution.ID || restarted.Execution.Generation != web.Execution.Generation+1 {
		t.Fatalf("restart did not create a new execution: %+v", restarted.Execution)
	}
	assertWeb(publicURL)
	assertCapacity(2)
	if got := readContainerFile(t, ctx, restarted.Execution.DockerContainerID, "cat /data/state/note.txt"); got != "cloudlet-takeover" {
		t.Fatalf("restarted volume content = %q", got)
	}
}

func readContainerImages(t *testing.T) struct {
	Postgres      string `json:"postgres"`
	ContainerHTTP string `json:"container_http"`
} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres      string `json:"postgres"`
		ContainerHTTP string `json:"container_http"`
	}
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}
	if images.Postgres == "" || images.ContainerHTTP == "" {
		t.Fatal("configs/images.json: postgres and container_http are required")
	}
	return images
}

func writeContainerFile(t *testing.T, ctx context.Context, containerID, command string) {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s: %v\n%s", containerID, err, output)
	}
}

func readContainerFile(t *testing.T, ctx context.Context, containerID, command string) string {
	t.Helper()
	output, err := exec.CommandContext(ctx, "docker", "exec", containerID, "sh", "-c", command).CombinedOutput()
	if err != nil {
		t.Fatalf("docker exec %s: %v\n%s", containerID, err, output)
	}
	return strings.TrimSpace(string(output))
}

func containerStartedAt(t *testing.T, ctx context.Context, docker *client.Client, containerID string) string {
	t.Helper()
	inspect, err := docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Container.State == nil || !inspect.Container.State.Running {
		t.Fatalf("container %s is not running", containerID)
	}
	return inspect.Container.State.StartedAt
}

func assertContainerCount(t *testing.T, ctx context.Context, docker *client.Client, containerID uuid.UUID, want int) {
	t.Helper()
	list, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: client.Filters{}.Add("label", containers.LabelResource+"="+containerID.String()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != want {
		t.Fatalf("containers for resource %s = %d, want %d", containerID, len(list.Items), want)
	}
}

func assertSameExecution(t *testing.T, ctx context.Context, service *containerpublish.Service, projectID uuid.UUID, original containerpublish.Result) {
	t.Helper()
	current, err := service.Get(ctx, projectID, original.Container.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Container.ID != original.Container.ID || current.Execution.ID != original.Execution.ID {
		t.Fatalf("takeover changed container %s execution %s to %s/%s",
			original.Container.ID, original.Execution.ID, current.Container.ID, current.Execution.ID)
	}
	if current.Execution.DockerContainerID != original.Execution.DockerContainerID {
		t.Fatalf("takeover changed Docker container %q to %q", original.Execution.DockerContainerID, current.Execution.DockerContainerID)
	}
	if current.Container.State != "active" || current.Execution.State != "running" {
		t.Fatalf("taken over container state = %s/%s", current.Container.State, current.Execution.State)
	}
}

func assertRouteTarget(t *testing.T, ctx context.Context, store *database.Store, containerID uuid.UUID, executionID uuid.UUID) {
	t.Helper()
	var executionTarget, routeTarget string
	if err := store.SQL.QueryRowContext(ctx, "SELECT target FROM container_executions WHERE id = $1", executionID).Scan(&executionTarget); err != nil {
		t.Fatal(err)
	}
	if err := store.SQL.QueryRowContext(ctx, "SELECT target FROM project_routes WHERE resource = $1", containerID.String()).Scan(&routeTarget); err != nil {
		t.Fatal(err)
	}
	if executionTarget != routeTarget || !strings.HasPrefix(executionTarget, "http://127.0.0.1:") {
		t.Fatalf("recorded endpoint execution=%q route=%q", executionTarget, routeTarget)
	}
}

func containerState(t *testing.T, ctx context.Context, store *database.Store, containerID uuid.UUID) string {
	t.Helper()
	var state string
	if err := store.SQL.QueryRowContext(ctx, "SELECT state FROM containers WHERE id = $1", containerID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func dockerVolumeExists(t *testing.T, ctx context.Context, docker *client.Client, name string) bool {
	t.Helper()
	if _, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err == nil {
		return true
	} else if !cerrdefs.IsNotFound(err) {
		t.Fatal(err)
	}
	return false
}

func cleanContainers(t *testing.T, ctx context.Context, docker *client.Client, instance string) {
	t.Helper()
	list, err := docker.ContainerList(ctx, client.ContainerListOptions{
		All: true, Filters: client.Filters{}.Add("label", containers.LabelEnvironment+"="+instance),
	})
	if err != nil {
		t.Error(err)
		return
	}
	for _, item := range list.Items {
		if _, err := docker.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Error(err)
		}
	}
	volumes, err := docker.VolumeList(ctx, client.VolumeListOptions{
		Filters: client.Filters{}.Add("label", containers.LabelEnvironment+"="+instance),
	})
	if err != nil {
		t.Error(err)
		return
	}
	for _, volume := range volumes.Items {
		if _, err := docker.VolumeRemove(ctx, volume.Name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Error(err)
		}
	}
}
