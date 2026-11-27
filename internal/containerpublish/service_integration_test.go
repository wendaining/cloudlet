//go:build integration

package containerpublish_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containerpublish"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/routes"
)

func TestContainerPublicationHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()

	imagesData, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres      string `json:"postgres"`
		ContainerHTTP string `json:"container_http"`
	}
	if err := json.Unmarshal(imagesData, &images); err != nil {
		t.Fatal(err)
	}
	if images.ContainerHTTP == "" {
		t.Fatal("configs/images.json: container_http is required")
	}
	postgresContainer, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("container-integration-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, postgresContainer)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
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
	t.Cleanup(func() {
		var executions []database.ContainerExecution
		if err := store.DB.Find(&executions).Error; err != nil {
			t.Error(err)
			return
		}
		for _, execution := range executions {
			if execution.DockerContainerID == "" {
				continue
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := docker.ContainerRemove(cleanupCtx, execution.DockerContainerID, client.ContainerRemoveOptions{Force: true})
			cleanupCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		}
		var volumes []string
		if err := store.DB.Model(&database.ContainerExecution{}).Where("volume_name <> ''").Pluck("volume_name", &volumes).Error; err != nil {
			t.Error(err)
			return
		}
		for _, volume := range volumes {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := docker.VolumeRemove(cleanupCtx, volume, client.VolumeRemoveOptions{Force: true})
			cleanupCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		}
	})

	front, err := gateway.NewHandler(config.Config{
		APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0",
		Routes: []config.Route{{Host: "reserved.localhost", Target: "http://127.0.0.1:1"}},
	}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	publisher, err := routes.NewPublisherWithSites(store, front, nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration := config.ContainerRuntime{
		HostID:       "container-" + uuid.NewString(),
		HostBudget:   config.ResourceBudget{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
		ProjectQuota: config.ResourceBudget{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4},
	}
	service, err := containerpublish.NewService(ctx, store, docker, publisher, configuration, "container-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	if duplicate, err := containerpublish.NewService(ctx, store, docker, publisher, configuration, "container-test"); err == nil {
		duplicate.Close()
		t.Fatal("second container controller accepted")
	}
	handler, closeSessions := controlplane.NewHandlerWithContainers(store, config.Config{Instance: "container-test", PublicOrigin: "http://localhost"}, slog.Default(), service)
	api := httptest.NewServer(handler)
	public := httptest.NewServer(front)
	t.Cleanup(func() {
		api.Close()
		public.Close()
		closeSessions()
	})

	const password = "container-integration-account-password"
	login := func(email string) (*http.Client, database.User) {
		t.Helper()
		if err := store.SeedUser(ctx, email, password); err != nil {
			t.Fatal(err)
		}
		user, err := store.Authenticate(ctx, email, password)
		if err != nil {
			t.Fatal(err)
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		browser := &http.Client{Jar: jar, Timeout: 4 * time.Minute}
		body, err := json.Marshal(apigen.LoginRequest{Email: email, Password: password})
		if err != nil {
			t.Fatal(err)
		}
		response, err := browser.Post(api.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("login: %d", response.StatusCode)
		}
		return browser, user
	}
	owner, ownerUser := login("container-owner@cloudlet.local")
	viewer, viewerUser := login("container-viewer@cloudlet.local")
	developer, developerUser := login("container-developer@cloudlet.local")
	outsider, _ := login("container-outsider@cloudlet.local")
	project, err := store.CreateProject(ctx, ownerUser.ID, "Container integration")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, project.ID, viewerUser.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, project.ID, developerUser.ID, "developer"); err != nil {
		t.Fatal(err)
	}

	request := func(browser *http.Client, method, path, key string, body any, expected int) (http.Header, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(data)
		}
		req, err := http.NewRequestWithContext(ctx, method, api.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != expected {
			t.Fatalf("%s %s: status=%d body=%s", method, path, response.StatusCode, data)
		}
		return response.Header, data
	}
	decode := func(data []byte) apigen.Container {
		t.Helper()
		var value apigen.Container
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	collectionPath := func(projectID string) string {
		return "/api/v1/projects/" + projectID + "/containers"
	}
	itemPath := func(projectID string, id uuid.UUID) string {
		return collectionPath(projectID) + "/" + id.String()
	}
	runInContainer := func(id, command string) string {
		t.Helper()
		output, err := exec.CommandContext(ctx, "docker", "exec", id, "sh", "-c", command).CombinedOutput()
		if err != nil {
			t.Fatalf("docker exec %s: %v\n%s", id, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	volumeExists := func(name string) bool {
		t.Helper()
		if _, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err == nil {
			return true
		} else if !cerrdefs.IsNotFound(err) {
			t.Fatal(err)
		}
		return false
	}

	valid := containerpublish.Request{
		Name: "web", Image: images.ContainerHTTP, Port: 8080, Hostname: "container.localhost",
		CPUMillis: 500, MemoryBytes: 128 << 20, User: "101:101",
		Env:        []string{"CLOUDLET_CONTAINER_TEST=1"},
		DataVolume: &containerpublish.DataVolumeRequest{MountPath: "/data", Directory: "state"},
	}
	path := collectionPath(project.ID)
	request(viewer, http.MethodPost, path, "viewer", valid, http.StatusForbidden)
	request(outsider, http.MethodPost, path, "outsider", valid, http.StatusNotFound)
	request(&http.Client{Timeout: 10 * time.Second}, http.MethodPost, path, "anonymous", valid, http.StatusUnauthorized)
	request(owner, http.MethodPost, path, "", valid, http.StatusBadRequest)
	request(owner, http.MethodPost, path, "unknown-fields", map[string]any{
		"name": "unknown", "image": images.ContainerHTTP, "port": 8080, "hostname": "unknown.localhost",
		"cpu_millis": 500, "memory_bytes": 128 << 20, "volumes": []string{},
	}, http.StatusBadRequest)
	tagOnly := valid
	tagOnly.Image = "nginx:latest"
	request(owner, http.MethodPost, path, "tag-only", tagOnly, http.StatusBadRequest)
	rootUser := valid
	rootUser.User = "0"
	request(owner, http.MethodPost, path, "root-user", rootUser, http.StatusBadRequest)
	withPort := valid
	withPort.Hostname = "container.localhost:8080"
	request(owner, http.MethodPost, path, "host-port", withPort, http.StatusBadRequest)
	reservedHost := valid
	reservedHost.Name, reservedHost.Hostname = "reserved", "reserved.localhost"
	request(owner, http.MethodPost, path, "reserved-host", reservedHost, http.StatusBadRequest)

	headers, data := request(owner, http.MethodPost, path, "web-key", valid, http.StatusCreated)
	created := decode(data)
	if created.State != "active" || created.ExecutionState != "running" || created.OperationOutcome != "succeeded" {
		t.Fatalf("container state: %+v", created)
	}
	if created.DockerContainerId == "" || created.Target == "" || !strings.HasPrefix(created.Target, "http://127.0.0.1:") {
		t.Fatalf("container endpoint: %+v", created)
	}
	if want := itemPath(project.ID, created.Id); headers.Get("Location") != want {
		t.Errorf("Location = %q, want %q", headers.Get("Location"), want)
	}
	if digest := images.ContainerHTTP[strings.Index(images.ContainerHTTP, "@")+1:]; created.ImageDigest != digest {
		t.Errorf("image digest = %q, want %q", created.ImageDigest, digest)
	}
	if created.User != "101:101" || created.Pids != 256 || created.OpenFiles != 1024 {
		t.Fatalf("container limits: %+v", created)
	}
	if created.Env == nil || !slices.Equal(*created.Env, []string{"CLOUDLET_CONTAINER_TEST=1"}) {
		t.Fatalf("container environment: %+v", created.Env)
	}
	if created.DataVolume == nil || created.DataVolume.Name == "" || created.DataVolume.MountPath != "/data" || created.DataVolume.Directory != "state" {
		t.Fatalf("container data volume: %+v", created.DataVolume)
	}
	volume, err := docker.VolumeInspect(ctx, created.DataVolume.Name, client.VolumeInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if volume.Volume.Labels[containers.LabelProject] != project.ID || volume.Volume.Labels[containers.LabelResource] != created.Id.String() || volume.Volume.Labels[containers.LabelVolume] != "writable" {
		t.Fatalf("data volume labels: %+v", volume.Volume.Labels)
	}
	runInContainer(created.DockerContainerId, "printf cloudlet-volume > /data/state/note.txt")

	inspect, err := docker.ContainerInspect(ctx, created.DockerContainerId, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Container.State == nil || !inspect.Container.State.Running {
		t.Fatalf("container is not running: %+v", inspect.Container.State)
	}
	if inspect.Container.Config.User != "101:101" || inspect.Container.HostConfig.NanoCPUs != 500*1_000_000 || inspect.Container.HostConfig.Memory != 128<<20 {
		t.Fatalf("container limits: user=%q cpu=%d memory=%d", inspect.Container.Config.User, inspect.Container.HostConfig.NanoCPUs, inspect.Container.HostConfig.Memory)
	}
	if !slices.Contains(inspect.Container.Config.Env, "CLOUDLET_CONTAINER_TEST=1") {
		t.Fatalf("container environment: %v", inspect.Container.Config.Env)
	}
	labels := inspect.Container.Config.Labels
	if labels[containers.LabelProject] != project.ID || labels[containers.LabelResource] != created.Id.String() || labels[containers.LabelGeneration] != "1" {
		t.Fatalf("container labels: %+v", labels)
	}
	published := false
	for port, bindings := range inspect.Container.NetworkSettings.Ports {
		if port.Num() != 8080 || port.Proto() != "tcp" {
			continue
		}
		published = len(bindings) > 0
		for _, binding := range bindings {
			if !binding.HostIP.IsLoopback() {
				t.Errorf("published port is not loopback: %s", binding.HostIP)
			}
		}
	}
	if !published {
		t.Fatal("container port 8080/tcp was not published")
	}

	publicClient := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(publicClient.CloseIdleConnections)
	getPublic := func(host string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		response, err := publicClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(strings.ToLower(string(body)), "nginx") {
			t.Fatalf("public container: status=%d body=%s", response.StatusCode, body)
		}
	}
	getPublic("CONTAINER.localhost.:8081")
	publicStatus := func(host string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		response, err := publicClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}

	adapter, err := containers.New(ctx, docker)
	if err != nil {
		t.Fatal(err)
	}
	otherOwnership := containers.Ownership{Environment: "container-test", Project: uuid.NewString(), Resource: created.Id.String(), Generation: 1}
	if _, err := adapter.EnsureDataVolume(ctx, otherOwnership, containers.DataVolume{Name: created.DataVolume.Name, MountPath: "/data", Directory: "state"}); !errors.Is(err, containers.ErrNotOwned) {
		t.Fatalf("another ownership accepted the data volume: %v", err)
	}

	_, replay := request(developer, http.MethodPost, path, "web-key", valid, http.StatusOK)
	replayed := decode(replay)
	if replayed.Id != created.Id || replayed.DockerContainerId != created.DockerContainerId {
		t.Fatalf("idempotent replay changed the container: %+v", replayed)
	}
	var executionCount int64
	if err := store.DB.Model(&database.ContainerExecution{}).Where("container_id = ?", created.Id).Count(&executionCount).Error; err != nil {
		t.Fatal(err)
	}
	if executionCount != 1 {
		t.Fatalf("container executions = %d, want 1", executionCount)
	}
	_, nameConflictBody := request(owner, http.MethodPost, path, "web-other-key", valid, http.StatusConflict)
	_, replayNameConflictBody := request(owner, http.MethodPost, path, "web-other-key", valid, http.StatusConflict)
	if string(replayNameConflictBody) != string(nameConflictBody) || !strings.Contains(string(nameConflictBody), database.ErrContainerAlreadyActive.Error()) {
		t.Fatalf("idempotent name conflict replay = %s, want %s", replayNameConflictBody, nameConflictBody)
	}
	differentRequest := valid
	differentRequest.CPUMillis = 600
	request(owner, http.MethodPost, path, "web-key", differentRequest, http.StatusConflict)
	_, data = request(viewer, http.MethodGet, itemPath(project.ID, created.Id), "", nil, http.StatusOK)
	if decode(data).Id != created.Id {
		t.Fatal("viewer read returned a different container")
	}
	request(outsider, http.MethodGet, itemPath(project.ID, created.Id), "", nil, http.StatusNotFound)
	request(owner, http.MethodGet, collectionPath(project.ID)+"/not-a-uuid", "", nil, http.StatusNotFound)

	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := capacityStore.ProjectUsage(ctx, uuid.MustParse(project.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != (capacity.Resources{CPUMillis: 500, MemoryBytes: 128 << 20, Instances: 1}) {
		t.Fatalf("project usage = %+v", usage.Used)
	}

	stopPath := itemPath(project.ID, created.Id) + "/stop"
	restartPath := itemPath(project.ID, created.Id) + "/restart"
	request(viewer, http.MethodPost, stopPath, "viewer-stop", nil, http.StatusForbidden)
	request(outsider, http.MethodPost, stopPath, "outsider-stop", nil, http.StatusNotFound)
	request(owner, http.MethodPost, stopPath, "", nil, http.StatusBadRequest)
	_, data = request(owner, http.MethodPost, stopPath, "web-stop", nil, http.StatusOK)
	stopped := decode(data)
	if stopped.State != "stopped" || stopped.ExecutionState != "stopped" || stopped.Target != "" {
		t.Fatalf("stopped container: %+v", stopped)
	}
	if status := publicStatus("container.localhost"); status != http.StatusNotFound {
		t.Fatalf("stopped container public status = %d", status)
	}
	if _, err := docker.ContainerInspect(ctx, created.DockerContainerId, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("stopped container still exists: %v", err)
	}
	if !volumeExists(created.DataVolume.Name) {
		t.Fatal("stopping the container removed its data volume")
	}
	usage, err = capacityStore.ProjectUsage(ctx, uuid.MustParse(project.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != (capacity.Resources{}) {
		t.Fatalf("stopped container retained capacity: %+v", usage.Used)
	}
	_, data = request(owner, http.MethodPost, stopPath, "web-stop", nil, http.StatusOK)
	if decode(data).State != "stopped" {
		t.Fatal("idempotent stop did not return the stopped state")
	}
	differentVolume := valid
	differentVolume.DataVolume = &containerpublish.DataVolumeRequest{MountPath: "/other", Directory: "state"}
	request(owner, http.MethodPost, path, "web-volume-change", differentVolume, http.StatusConflict)
	_, data = request(viewer, http.MethodGet, itemPath(project.ID, created.Id), "", nil, http.StatusOK)
	if decode(data).State != "stopped" {
		t.Fatal("stopped container is not readable")
	}

	request(viewer, http.MethodPost, restartPath, "viewer-restart", nil, http.StatusForbidden)
	_, data = request(owner, http.MethodPost, restartPath, "web-restart", nil, http.StatusOK)
	restarted := decode(data)
	if restarted.State != "active" || restarted.ExecutionState != "running" || restarted.Generation != 2 {
		t.Fatalf("restarted container: %+v", restarted)
	}
	if restarted.DockerContainerId == "" || restarted.DockerContainerId == created.DockerContainerId {
		t.Fatalf("restart Docker container = %q, want a new container", restarted.DockerContainerId)
	}
	if restarted.DataVolume == nil || restarted.DataVolume.Name != created.DataVolume.Name {
		t.Fatalf("restarted data volume = %+v, want %s", restarted.DataVolume, created.DataVolume.Name)
	}
	if got := runInContainer(restarted.DockerContainerId, "cat /data/state/note.txt"); got != "cloudlet-volume" {
		t.Fatalf("persisted volume content = %q", got)
	}
	getPublic("container.localhost")
	request(owner, http.MethodPost, restartPath, "web-restart-active", nil, http.StatusConflict)
	usage, err = capacityStore.ProjectUsage(ctx, uuid.MustParse(project.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != (capacity.Resources{CPUMillis: 500, MemoryBytes: 128 << 20, Instances: 1}) {
		t.Fatalf("restarted container capacity = %+v", usage.Used)
	}

	request(viewer, http.MethodDelete, itemPath(project.ID, created.Id), "viewer-delete", nil, http.StatusForbidden)
	request(outsider, http.MethodDelete, itemPath(project.ID, created.Id), "outsider-delete", nil, http.StatusNotFound)
	request(owner, http.MethodDelete, itemPath(project.ID, created.Id), "", nil, http.StatusBadRequest)
	request(owner, http.MethodDelete, itemPath(project.ID, created.Id), "web-delete", nil, http.StatusNoContent)
	if status := publicStatus("container.localhost"); status != http.StatusNotFound {
		t.Fatalf("deleted container public status = %d", status)
	}
	request(viewer, http.MethodGet, itemPath(project.ID, created.Id), "", nil, http.StatusNotFound)
	var deletedContainers, deletedExecutions int64
	if err := store.DB.Model(&database.Container{}).Where("project_id = ? AND id = ?", project.ID, created.Id).Count(&deletedContainers).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Model(&database.ContainerExecution{}).Where("project_id = ? AND container_id = ?", project.ID, created.Id).Count(&deletedExecutions).Error; err != nil {
		t.Fatal(err)
	}
	if deletedContainers != 0 || deletedExecutions != 0 {
		t.Fatalf("deleted container rows = %d, executions = %d", deletedContainers, deletedExecutions)
	}
	if _, err := docker.ContainerInspect(ctx, restarted.DockerContainerId, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("deleted container still exists: %v", err)
	}
	if volumeExists(created.DataVolume.Name) {
		t.Fatal("deleting the container retained its data volume")
	}
	usage, err = capacityStore.ProjectUsage(ctx, uuid.MustParse(project.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != (capacity.Resources{}) {
		t.Fatalf("deleted container retained capacity: %+v", usage.Used)
	}
	request(owner, http.MethodDelete, itemPath(project.ID, created.Id), "web-delete", nil, http.StatusNoContent)

	failureProject, err := store.CreateProject(ctx, ownerUser.ID, "Container failure")
	if err != nil {
		t.Fatal(err)
	}
	broken := valid
	broken.Name, broken.Hostname = "broken", "broken.localhost"
	broken.Command = []string{"/bin/sh", "-c", "exit 1"}
	broken.HealthCheckTimeoutMS = 3000
	_, data = request(owner, http.MethodPost, collectionPath(failureProject.ID), "broken-key", broken, http.StatusUnprocessableEntity)
	brokenResult := decode(data)
	if brokenResult.State != "failed" || brokenResult.ExecutionState != "failed" || brokenResult.OperationOutcome != "failed" || brokenResult.Failure == "" {
		t.Fatalf("failed container state: %+v", brokenResult)
	}
	if brokenResult.DockerContainerId == "" {
		t.Fatal("failed container did not record its Docker container")
	}
	_, replay = request(owner, http.MethodPost, collectionPath(failureProject.ID), "broken-key", broken, http.StatusOK)
	if replayedFailure := decode(replay); replayedFailure.State != "failed" {
		t.Fatalf("failed container replay did not return the recorded failure: %+v", replayedFailure)
	}
	if _, err := docker.ContainerInspect(ctx, brokenResult.DockerContainerId, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("failed container still exists: %v", err)
	}
	if brokenResult.DataVolume == nil || volumeExists(brokenResult.DataVolume.Name) {
		t.Fatalf("failed container retained its data volume: %+v", brokenResult.DataVolume)
	}
	usage, err = capacityStore.ProjectUsage(ctx, uuid.MustParse(failureProject.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != (capacity.Resources{}) {
		t.Fatalf("failed container retained capacity: %+v", usage.Used)
	}

	tooBig := valid
	tooBig.Name, tooBig.Hostname, tooBig.CPUMillis = "too-big", "too-big.localhost", 5000
	_, data = request(owner, http.MethodPost, collectionPath(failureProject.ID), "too-big-key", tooBig, http.StatusTooManyRequests)
	tooBigResult := decode(data)
	if tooBigResult.State != "failed" || tooBigResult.OperationOutcome != "failed" || tooBigResult.DockerContainerId != "" {
		t.Fatalf("over-quota container: %+v", tooBigResult)
	}
	if tooBigResult.DataVolume == nil || volumeExists(tooBigResult.DataVolume.Name) {
		t.Fatalf("over-quota container retained its data volume: %+v", tooBigResult.DataVolume)
	}

	if _, err := publisher.PublishProject(ctx, failureProject.ID, []routes.Route{{Resource: "site", Host: "taken.localhost", Target: "http://127.0.0.1:1"}}); err != nil {
		t.Fatal(err)
	}
	conflict := valid
	conflict.Name, conflict.Hostname = "conflict", "taken.localhost"
	_, conflictBody := request(owner, http.MethodPost, collectionPath(project.ID), "conflict-key", conflict, http.StatusConflict)
	if !strings.Contains(string(conflictBody), database.ErrRouteConflict.Error()) {
		t.Fatalf("route conflict response = %s", conflictBody)
	}
	_, replayConflictBody := request(owner, http.MethodPost, collectionPath(project.ID), "conflict-key", conflict, http.StatusConflict)
	if string(replayConflictBody) != string(conflictBody) {
		t.Fatalf("idempotent conflict replay = %s, want %s", replayConflictBody, conflictBody)
	}
	var conflictCount int64
	if err := store.DB.Model(&database.Container{}).Where("project_id = ? AND name = ?", project.ID, "conflict").Count(&conflictCount).Error; err != nil {
		t.Fatal(err)
	}
	if conflictCount != 0 {
		t.Fatalf("route conflict created %d container rows", conflictCount)
	}

	retry := valid
	retry.Name, retry.Hostname = "broken", "broken-retry.localhost"
	retry.Command = nil
	_, data = request(owner, http.MethodPost, collectionPath(failureProject.ID), "broken-retry-key", retry, http.StatusCreated)
	retried := decode(data)
	if retried.State != "active" || retried.Generation != 2 || retried.DockerContainerId == "" {
		t.Fatalf("retry after failure: %+v", retried)
	}
}
