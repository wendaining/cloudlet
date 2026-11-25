//go:build integration

package workerpublish_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	openapi_types "github.com/oapi-codegen/runtime/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/capacity"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/containers"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/gateway"
	"github.com/wendaining/cloudlet/internal/operations"
	"github.com/wendaining/cloudlet/internal/routes"
	"github.com/wendaining/cloudlet/internal/workerpublish"
	"github.com/wendaining/cloudlet/internal/workers"
	sharedclient "github.com/wendaining/cloudlet/pkg/client"
)

func TestWorkerPublicationHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	imagesData, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(imagesData, &images); err != nil {
		t.Fatal(err)
	}
	postgresContainer, err := postgres.Run(ctx, images.Postgres, postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"), postgres.WithPassword("worker-integration-password"), testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
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
		var volumes []database.WorkerDurableStorage
		if err := store.DB.Find(&volumes).Error; err != nil {
			t.Error(err)
			return
		}
		for _, volume := range volumes {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := docker.VolumeRemove(cleanupCtx, volume.VolumeName, client.VolumeRemoveOptions{})
			cancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		}
	})
	t.Cleanup(func() {
		var executions []database.WorkerExecution
		if err := store.DB.Find(&executions).Error; err != nil {
			t.Error(err)
			return
		}
		for _, execution := range executions {
			if execution.ContainerID == "" {
				continue
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, err := docker.ContainerRemove(cleanupCtx, execution.ContainerID, client.ContainerRemoveOptions{Force: true})
			cleanupCancel()
			if err != nil && !cerrdefs.IsNotFound(err) {
				t.Error(err)
			}
		}
	})
	configuration := config.WorkerRuntime{Directory: t.TempDir(), HostID: "publication-" + uuid.NewString(), HostBudget: config.ResourceBudget{CPUMillis: 4000, MemoryBytes: 4 << 30, Instances: 4}, ProjectQuota: config.ResourceBudget{CPUMillis: 3000, MemoryBytes: 1 << 30, Instances: 3}}
	newFront := func() *gateway.Handler {
		front, err := gateway.NewHandler(config.Config{APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		return front
	}
	newPublisher := func(front *gateway.Handler) *routes.Publisher {
		t.Helper()
		publisher, err := routes.NewPublisher(store, front)
		if err != nil {
			t.Fatal(err)
		}
		return publisher
	}
	front := newFront()
	publisher := newPublisher(front)
	publication, err := workerpublish.NewService(ctx, store, docker, publisher, configuration, "publication")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := workerpublish.NewService(ctx, store, docker, publisher, configuration, "publication"); err == nil {
		duplicate.Close()
		t.Fatal("second controller accepted")
	}
	cfg := config.Config{Instance: "publication", PublicOrigin: "http://localhost"}
	handler, closeSessions := controlplane.NewHandlerWithWorkers(store, cfg, slog.Default(), publication)
	api := httptest.NewServer(handler)
	public := httptest.NewServer(front)
	t.Cleanup(func() {
		api.Close()
		closeSessions()
		public.Close()
		if publication != nil {
			if err := publication.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	const password = "worker-integration-account-password"
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
		browser := &http.Client{Jar: jar, Timeout: 70 * time.Second}
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
	owner, user := login("worker-owner@cloudlet.local")
	viewer, viewerUser := login("worker-viewer@cloudlet.local")
	developer, developerUser := login("worker-developer@cloudlet.local")
	outsider, outsiderUser := login("worker-outsider@cloudlet.local")
	project, err := store.CreateProject(ctx, user.ID, "Worker integration")
	if err != nil {
		t.Fatal(err)
	}
	otherProject, err := store.CreateProject(ctx, outsiderUser.ID, "Other project")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, project.ID, viewerUser.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertProjectMember(ctx, project.ID, developerUser.ID, "developer"); err != nil {
		t.Fatal(err)
	}
	requestWithLocation := func(browser *http.Client, method, path, key string, body any, expected int) (string, []byte) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, method, api.URL+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err = io.ReadAll(response.Body)
		if err != nil || response.StatusCode != expected {
			t.Fatalf("%s %s: status=%d body=%s error=%v", method, path, response.StatusCode, data, err)
		}
		return response.Header.Get("Location"), data
	}
	request := func(browser *http.Client, method, path, key string, body any, expected int) []byte {
		t.Helper()
		_, data := requestWithLocation(browser, method, path, key, body, expected)
		return data
	}
	decode := func(data []byte) apigen.WorkerDeployment {
		t.Helper()
		var value apigen.WorkerDeployment
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	deployPath := func(projectID, name string) string {
		return "/api/v1/projects/" + projectID + "/workers/" + name + "/deployments"
	}
	metadataPath := func(projectID string, id uuid.UUID) string {
		return "/api/v1/projects/" + projectID + "/worker-deployments/" + id.String()
	}
	module := `export default { async fetch(request) { const url = new URL(request.url); return new Response(request.method + " " + url.pathname + " " + await request.text(), { headers: { "X-Worker": "workerd" } }); } };`
	valid := workerpublish.Request{Module: module, CompatibilityDate: "2026-10-12", Hostname: "worker.localhost"}
	path := deployPath(project.ID, "echo")
	request(viewer, http.MethodPost, path, "viewer", valid, http.StatusForbidden)
	request(outsider, http.MethodPost, path, "outsider", valid, http.StatusNotFound)
	request(&http.Client{}, http.MethodPost, path, "anonymous", valid, http.StatusUnauthorized)
	request(owner, http.MethodPost, path, "", valid, http.StatusBadRequest)
	request(owner, http.MethodPost, path, "unknown-fields", map[string]any{"module": module, "compatibility_date": "2026-10-12", "hostname": "worker.localhost", "bindings": []string{"secret"}}, http.StatusBadRequest)
	initial := decode(request(owner, http.MethodPost, path, "first-publication", valid, http.StatusCreated))
	if initial.State != "active" || initial.OperationOutcome != "succeeded" || initial.RuntimeVersion != workers.RuntimeVersion || initial.ArtifactDigest != fmt.Sprintf("%x", sha256.Sum256([]byte(module))) || initial.ContainerId == "" {
		t.Fatalf("publication result: %+v", initial)
	}
	inspect, err := docker.ContainerInspect(ctx, initial.ContainerId, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspect.Container.Config.User != "1000:1000" || inspect.Container.Config.Labels["io.cloudlet.resource"] != initial.WorkerId.String() || inspect.Container.HostConfig.Memory != 256<<20 {
		t.Fatalf("runtime identity and limits: %+v", inspect.Container)
	}
	getPublic := func() {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, public.URL+"/echo", strings.NewReader("cloudlet"))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "WORKER.localhost.:8081"
		response, err := owner.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || string(data) != "POST /echo cloudlet" || response.Header.Get("X-Worker") != "workerd" {
			t.Fatalf("public Worker: status=%d body=%s err=%v", response.StatusCode, data, err)
		}
		t.Logf("public workerd: HTTP %d, body=%s, container=%s", response.StatusCode, data, initial.ContainerId)
	}
	getPublic()
	shared, err := sharedclient.New(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	apiURL, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	cookieRequest := &http.Request{Header: make(http.Header)}
	for _, cookie := range owner.Jar.Cookies(apiURL) {
		cookieRequest.AddCookie(cookie)
	}
	if err := shared.SetSessionCookie(cookieRequest.Header.Get("Cookie")); err != nil {
		t.Fatal(err)
	}
	duplicate, err := shared.PublishWorker(ctx, project.ID, "echo", "first-publication", sharedclient.WorkerPublicationRequest{Module: module, Hostname: valid.Hostname, CompatibilityDate: openapi_types.Date{Time: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}})
	if err != nil || duplicate.Id != initial.Id {
		t.Fatalf("shared publication client: %+v %v", duplicate, err)
	}
	clientMetadata, err := shared.GetWorkerDeployment(ctx, project.ID, initial.Id.String())
	if err != nil || clientMetadata.ContainerId != initial.ContainerId {
		t.Fatalf("shared metadata client: %+v %v", clientMetadata, err)
	}
	metadata := decode(request(viewer, http.MethodGet, metadataPath(project.ID, initial.Id), "", nil, http.StatusOK))
	if metadata.Id != initial.Id || metadata.ArtifactDigest != initial.ArtifactDigest {
		t.Fatal("metadata changed")
	}
	request(outsider, http.MethodGet, metadataPath(project.ID, initial.Id), "", nil, http.StatusNotFound)
	request(outsider, http.MethodGet, metadataPath(otherProject.ID, initial.Id), "", nil, http.StatusNotFound)
	operationStore, err := operations.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	pendingRequest := valid
	pendingRequest.Hostname = "pending.localhost"
	pendingData, err := json.Marshal(pendingRequest)
	if err != nil {
		t.Fatal(err)
	}
	pendingOperation, _, err := operationStore.Register(ctx, operations.Registration{ProjectID: uuid.MustParse(project.ID), Action: "worker.deploy:pending", IdempotencyKey: "pending-stage", RequestFingerprint: fmt.Sprintf("%x", sha256.Sum256(pendingData))})
	if err != nil {
		t.Fatal(err)
	}
	pending, _, err := store.WorkerDeployment(ctx, uuid.MustParse(project.ID), initial.Id)
	if err != nil {
		t.Fatal(err)
	}
	pending.ID, pending.OperationID, pending.State, pending.Hostname = uuid.New(), pendingOperation.ID, "pending", "pending.localhost"
	if _, _, err := store.CreateWorkerDeployment(ctx, "pending", pending); err != nil {
		t.Fatal(err)
	}
	location, _ := requestWithLocation(owner, http.MethodPost, deployPath(project.ID, "pending"), "second-pending", pendingRequest, http.StatusConflict)
	if location != "" {
		t.Fatalf("new conflicting operation returned an unregistered deployment URL: %s", location)
	}
	location, _ = requestWithLocation(owner, http.MethodPost, deployPath(project.ID, "pending"), "pending-stage", pendingRequest, http.StatusConflict)
	if want := metadataPath(project.ID, pending.ID); location != want {
		t.Errorf("pending publication Location = %q, want %q", location, want)
	} else {
		pendingMetadata := decode(request(owner, http.MethodGet, location, "", nil, http.StatusOK))
		if pendingMetadata.Id != pending.ID || pendingMetadata.OperationId != pendingOperation.ID || pendingMetadata.State != "pending" || pendingMetadata.OperationOutcome != "in_progress" {
			t.Fatalf("pending publication metadata: %+v", pendingMetadata)
		}
	}
	var wg sync.WaitGroup
	var results [4]apigen.WorkerDeployment
	var failures [4]error
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for index := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+path, bytes.NewReader(data))
			if err != nil {
				failures[index] = err
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", "first-publication")
			response, err := owner.Do(req)
			if err != nil {
				failures[index] = err
				return
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				failures[index] = fmt.Errorf("duplicate status %d", response.StatusCode)
				return
			}
			failures[index] = json.NewDecoder(response.Body).Decode(&results[index])
		}()
	}
	wg.Wait()
	for index := range results {
		if failures[index] != nil || results[index].Id != initial.Id || results[index].ContainerId != initial.ContainerId {
			t.Fatalf("duplicate publication %d: %+v %v", index, results[index], failures[index])
		}
	}
	changed := valid
	changed.Module += "\n// next version"
	request(owner, http.MethodPost, path, "first-publication", changed, http.StatusConflict)
	request(owner, http.MethodPost, path, "new-version", changed, http.StatusConflict)
	getPublic()
	syntax := valid
	syntax.Module = "export default {"
	syntax.Hostname = "syntax.localhost"
	failed := decode(request(owner, http.MethodPost, deployPath(project.ID, "syntax"), "syntax", syntax, http.StatusUnprocessableEntity))
	if failed.State != "failed" || failed.OperationOutcome != "failed" || !strings.Contains(failed.Failure, "load") || failed.ExecutionState != "stopped" {
		t.Fatalf("preflight failure: %+v", failed)
	}
	failedRetry := decode(request(owner, http.MethodPost, deployPath(project.ID, "syntax"), "syntax", syntax, http.StatusOK))
	if failedRetry.Id != failed.Id || failedRetry.OperationId != failed.OperationId || failedRetry.Failure != failed.Failure {
		t.Fatal("repeated failed publication changed its recorded result")
	}
	clientFailed, err := shared.PublishWorker(ctx, project.ID, "syntax", "syntax", sharedclient.WorkerPublicationRequest{Module: syntax.Module, Hostname: syntax.Hostname, CompatibilityDate: openapi_types.Date{Time: time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)}})
	if err == nil || clientFailed.Id != failed.Id {
		t.Fatalf("shared client did not report the failed operation: %+v %v", clientFailed, err)
	}
	syntax.Module = module
	retried := decode(request(owner, http.MethodPost, deployPath(project.ID, "syntax"), "valid-retry", syntax, http.StatusCreated))
	if retried.WorkerId != failed.WorkerId || retried.Generation != 2 {
		t.Fatalf("retry changed Worker identity: %+v %+v", failed, retried)
	}
	initialization := valid
	initialization.Module = `throw new Error("publication initialization failure"); export default { fetch() { return new Response("ready") } };`
	initialization.Hostname = "initialize.localhost"
	initializationFailure := decode(request(owner, http.MethodPost, deployPath(project.ID, "initialize"), "initialize", initialization, http.StatusUnprocessableEntity))
	if !strings.Contains(initializationFailure.Failure, "initialize") || initializationFailure.ExecutionState != "stopped" {
		t.Fatalf("initialize failure: %+v", initializationFailure)
	}
	unhealthy := valid
	unhealthy.Module = `export default { fetch() { return new Response("unhealthy", {status:503}) } };`
	unhealthy.Hostname = "unhealthy.localhost"
	unhealthyFailure := decode(request(owner, http.MethodPost, deployPath(project.ID, "unhealthy"), "unhealthy", unhealthy, http.StatusUnprocessableEntity))
	if !strings.Contains(unhealthyFailure.Failure, "HTTP 503") || unhealthyFailure.ExecutionState != "stopped" {
		t.Fatalf("readiness failure: %+v", unhealthyFailure)
	}
	foreign := valid
	location, _ = requestWithLocation(outsider, http.MethodPost, deployPath(otherProject.ID, "foreign"), "foreign", foreign, http.StatusConflict)
	if location != "" {
		t.Fatalf("early route conflict returned a deployment URL: %s", location)
	}
	lateConflict := valid
	lateConflict.Hostname = "late-conflict.localhost"
	lateConflict.Module = `let readyAt = 0;
export default {
  fetch() {
    if (readyAt === 0) readyAt = Date.now() + 8000;
    return new Response("delayed Worker", {status: Date.now() < readyAt ? 503 : 200});
  }
};`
	lateData, err := json.Marshal(lateConflict)
	if err != nil {
		t.Fatal(err)
	}
	lateRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, api.URL+deployPath(project.ID, "late-conflict"), bytes.NewReader(lateData))
	if err != nil {
		t.Fatal(err)
	}
	lateRequest.Header.Set("Content-Type", "application/json")
	lateRequest.Header.Set("Idempotency-Key", "late-conflict")
	type publicationResponse struct {
		status   int
		location string
		body     []byte
		err      error
	}
	publicationDone := make(chan publicationResponse, 1)
	go func() {
		response, err := owner.Do(lateRequest)
		if err != nil {
			publicationDone <- publicationResponse{err: err}
			return
		}
		data, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		publicationDone <- publicationResponse{status: response.StatusCode, location: response.Header.Get("Location"), body: data, err: errors.Join(readErr, closeErr)}
	}()
	var lateDeploymentID uuid.UUID
	var lateContainerID string
	preparingDeadline := time.NewTimer(20 * time.Second)
	defer preparingDeadline.Stop()
	preparingTick := time.NewTicker(20 * time.Millisecond)
	defer preparingTick.Stop()
preparing:
	for {
		select {
		case <-preparingDeadline.C:
			t.Fatal("Worker did not reach its actual preparing stage")
		case response := <-publicationDone:
			t.Fatalf("Worker publication completed before the route claim: %+v", response)
		case <-preparingTick.C:
			err := store.DB.WithContext(ctx).Raw(`SELECT d.id, e.container_id
FROM worker_deployments d
JOIN workers w ON w.id = d.worker_id
JOIN worker_executions e ON e.deployment_id = d.id
JOIN operations o ON o.id = d.operation_id
WHERE d.project_id = ? AND w.name = 'late-conflict'
AND d.state = 'pending' AND o.phase = 'preparing' AND e.container_id <> ''`, project.ID).Row().Scan(&lateDeploymentID, &lateContainerID)
			if err == nil {
				break preparing
			}
			if !errors.Is(err, sql.ErrNoRows) {
				t.Fatal(err)
			}
		}
	}
	opponent, err := routes.NewPublisher(store, front)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := opponent.PublishProject(ctx, otherProject.ID, []routes.Route{{Resource: "management-health", Host: lateConflict.Hostname, Target: api.URL}}); err != nil {
		t.Fatal(err)
	}
	var lateResponse publicationResponse
	select {
	case lateResponse = <-publicationDone:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if lateResponse.err != nil || lateResponse.status != http.StatusConflict {
		t.Fatalf("activation conflict response: %+v", lateResponse)
	}
	if want := metadataPath(project.ID, lateDeploymentID); lateResponse.location != want {
		t.Errorf("activation conflict Location = %q, want %q", lateResponse.location, want)
	} else {
		failedMetadata := decode(request(owner, http.MethodGet, lateResponse.location, "", nil, http.StatusOK))
		if failedMetadata.Id != lateDeploymentID || failedMetadata.State != "failed" || failedMetadata.OperationPhase != "activating" || failedMetadata.OperationOutcome != "failed" || failedMetadata.ExecutionState != "stopped" || !strings.Contains(failedMetadata.Failure, "route conflict") {
			t.Fatalf("activation conflict metadata: %+v", failedMetadata)
		}
		request(outsider, http.MethodGet, lateResponse.location, "", nil, http.StatusNotFound)
	}
	if _, err := docker.ContainerInspect(ctx, lateContainerID, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("activation conflict left its Worker container: %v", err)
	}
	capacityStore, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	usageAfterConflict, err := capacityStore.Usage(ctx, configuration.HostID, uuid.MustParse(project.ID))
	if err != nil || usageAfterConflict.ProjectUsed.CPUMillis != 2000 || usageAfterConflict.ProjectUsed.Instances != 2 {
		t.Fatalf("activation conflict capacity: %+v %v", usageAfterConflict, err)
	}
	healthRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, public.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	healthRequest.Host = lateConflict.Hostname
	healthResponse, err := owner.Do(healthRequest)
	if err != nil {
		t.Fatal(err)
	}
	healthBody, readErr := io.ReadAll(healthResponse.Body)
	closeErr := healthResponse.Body.Close()
	if readErr != nil || closeErr != nil || healthResponse.StatusCode != http.StatusOK || !bytes.Contains(healthBody, []byte(`"status":"ok"`)) {
		t.Fatalf("claimed project route: HTTP %d body=%s error=%v", healthResponse.StatusCode, healthBody, errors.Join(readErr, closeErr))
	}
	getPublic()
	third := valid
	third.Hostname = "third.localhost"
	decode(request(developer, http.MethodPost, deployPath(project.ID, "third"), "third", third, http.StatusCreated))
	fourth := valid
	fourth.Hostname = "fourth.localhost"
	quotaFailure := decode(request(owner, http.MethodPost, deployPath(project.ID, "fourth"), "fourth", fourth, http.StatusTooManyRequests))
	if quotaFailure.State != "failed" || quotaFailure.ContainerId != "" {
		t.Fatalf("quota failure: %+v", quotaFailure)
	}
	reservations, err := capacity.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := reservations.Usage(ctx, configuration.HostID, uuid.MustParse(project.ID))
	if err != nil {
		t.Fatal(err)
	}
	if usage.ProjectUsed.CPUMillis != 3000 || usage.ProjectUsed.Instances != 3 {
		t.Fatalf("failed publication leaked capacity: %+v", usage)
	}
	t.Run("publish and restart a Worker with Durable Objects", func(t *testing.T) {
		const hostname = "objects.localhost"
		module := `import { DurableObject } from "cloudflare:workers";
export class ObjectStore extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    this.sql = ctx.storage.sql;
    this.sql.exec("CREATE TABLE IF NOT EXISTS data (value TEXT PRIMARY KEY)");
  }
  set(value, commit) {
    try {
      this.ctx.storage.transactionSync(() => {
        this.sql.exec("DELETE FROM data");
        this.sql.exec("INSERT INTO data VALUES (?)", value);
        if (!commit) throw new Error("rollback");
      });
    } catch (err) {
      if (commit || err.message !== "rollback") throw err;
    }
  }
  get() {
    return this.sql.exec("SELECT value FROM data").toArray()[0]?.value ?? null;
  }
}
export default {
  async fetch(request, env) {
    const name = new URL(request.url).pathname.slice(1);
    if (!name) return new Response("ready");
    const id = env.STORE.idFromName(name);
    const object = env.STORE.get(id);
    if (request.method === "PUT") await object.set(await request.text(), true);
    if (request.method === "PATCH") await object.set(await request.text(), false);
    return Response.json({ id: id.toString(), value: await object.get() });
  }
};`
		declared := []workerpublish.DurableObjectBinding{{ClassName: "ObjectStore", Bindings: []string{"STORE"}}}
		input := workerpublish.Request{Module: module, CompatibilityDate: valid.CompatibilityDate, Hostname: hostname, DurableObjects: declared}
		path := deployPath(otherProject.ID, "objects")
		missingClass := input
		missingClass.DurableObjects = []workerpublish.DurableObjectBinding{{ClassName: "Missing", Bindings: []string{"STORE"}}}
		invalid := decode(request(outsider, http.MethodPost, path, "missing-class", missingClass, http.StatusUnprocessableEntity))
		if invalid.OperationOutcome != "failed" || !strings.Contains(invalid.Failure, "Missing Durable Object class export: Missing") {
			t.Fatalf("missing class preflight result: %+v", invalid)
		}
		badBinding := input
		badBinding.DurableObjects = []workerpublish.DurableObjectBinding{{ClassName: "ObjectStore", Bindings: []string{"STORE", "STORE"}}}
		request(outsider, http.MethodPost, path, "invalid-binding", badBinding, http.StatusBadRequest)
		published := decode(request(outsider, http.MethodPost, path, "with-objects", input, http.StatusCreated))
		if published.State != "active" || published.OperationOutcome != "succeeded" {
			t.Fatalf("publish Durable Objects: %+v", published)
		}
		request(outsider, http.MethodPost, path, "second-writer", input, http.StatusConflict)
		var ownership database.WorkerDurableStorage
		if err := store.DB.Where("worker_id = ?", published.WorkerId).Take(&ownership).Error; err != nil {
			t.Fatal(err)
		}
		var namespace database.WorkerDurableNamespace
		if err := store.DB.Where("worker_id = ?", published.WorkerId).Take(&namespace).Error; err != nil {
			t.Fatal(err)
		}
		if ownership.ProjectID.String() != otherProject.ID || ownership.OwnerExecutionID == nil || *ownership.OwnerExecutionID != published.ExecutionId ||
			namespace.ProjectID != ownership.ProjectID || namespace.WorkerID != ownership.WorkerID || namespace.ClassName != "ObjectStore" || namespace.ID == uuid.Nil {
			t.Fatalf("Durable Object registration: %+v %+v", ownership, namespace)
		}
		// Exercise the storage guard directly. The first-publication API rejects
		// another deployment before it reaches the storage claim.
		competingOperation, _, err := operationStore.Register(ctx, operations.Registration{
			ProjectID: ownership.ProjectID, Action: "worker.deploy:objects", IdempotencyKey: "competing-claim",
			RequestFingerprint: "competing-claim",
		})
		if err != nil {
			t.Fatal(err)
		}
		competingDeployment, competingExecution, err := store.WorkerDeployment(ctx, ownership.ProjectID, published.Id)
		if err != nil {
			t.Fatal(err)
		}
		competingDeployment.ID, competingDeployment.OperationID, competingDeployment.State = uuid.New(), competingOperation.ID, "pending"
		competingExecution.ID, competingExecution.DeploymentID = uuid.New(), competingDeployment.ID
		competingExecution.Generation++
		competingExecution.ContainerID, competingExecution.State = "", "pending"
		if err := store.DB.Create(&competingDeployment).Error; err != nil {
			t.Fatal(err)
		}
		if err := store.DB.Create(&competingExecution).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimWorkerDurableStorage(ctx, competingExecution, nil); !errors.Is(err, database.ErrWorkerStorageOwned) {
			t.Fatalf("competing execution claimed a writable volume: %v", err)
		}
		if err := store.DB.Where("worker_id = ?", published.WorkerId).Take(&ownership).Error; err != nil {
			t.Fatal(err)
		}
		if ownership.OwnerExecutionID == nil || *ownership.OwnerExecutionID != published.ExecutionId {
			t.Fatalf("competing claim changed the storage owner: %+v", ownership)
		}
		worker, err := docker.ContainerInspect(ctx, published.ContainerId, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if worker.Container.Config.User != "1000:1000" || len(worker.Container.Mounts) != 1 || worker.Container.Mounts[0].Name != ownership.VolumeName || !worker.Container.Mounts[0].RW {
			t.Fatalf("unexpected mounted storage: %+v", worker.Container.Mounts)
		}
		type objectValue struct {
			ID    string
			Value *string
		}
		call := func(endpoint, method, name, value string) objectValue {
			t.Helper()
			req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+name, strings.NewReader(value))
			if err != nil {
				t.Fatal(err)
			}
			req.Host = hostname
			response, err := outsider.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("Durable Object %s returned HTTP %d: %s (%v)", name, response.StatusCode, body, err)
			}
			var result objectValue
			if err := json.Unmarshal(body, &result); err != nil || result.ID == "" {
				t.Fatalf("decode object %s: %s (%v)", name, body, err)
			}
			return result
		}
		assertValue := func(object objectValue, id, value string) {
			t.Helper()
			if object.ID != id || object.Value == nil || *object.Value != value {
				t.Fatalf("object ID and committed value: %+v, want %s and %s", object, id, value)
			}
		}
		alpha := call(public.URL, http.MethodPut, "alpha", "first")
		if empty := call(public.URL, http.MethodGet, "beta", ""); empty.ID == alpha.ID || empty.Value != nil {
			t.Fatalf("object IDs share state: %+v %+v", alpha, empty)
		}
		beta := call(public.URL, http.MethodPut, "beta", "second")
		assertValue(call(public.URL, http.MethodPatch, "alpha", "rolled back"), alpha.ID, "first")
		if _, err := docker.ContainerRestart(ctx, published.ContainerId, client.ContainerRestartOptions{}); err != nil {
			t.Fatal(err)
		}
		restarted, err := docker.ContainerInspect(ctx, published.ContainerId, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !restarted.Container.State.Running || restarted.Container.State.StartedAt == worker.Container.State.StartedAt {
			t.Fatalf("Docker did not restart the Worker: %+v", restarted.Container.State)
		}
		executor, err := containers.New(ctx, docker)
		if err != nil {
			t.Fatal(err)
		}
		readiness, err := executor.WaitReady(ctx, containers.Ownership{
			Environment: "publication", Project: otherProject.ID, Resource: published.WorkerId.String(), Generation: uint64(published.Generation),
		}, published.ContainerId, containers.ReadinessProbe{
			Port: workers.HTTPPort, Path: "/", Timeout: 10 * time.Second, Interval: 50 * time.Millisecond, Statuses: []int{http.StatusOK},
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := strings.TrimSuffix(readiness.Endpoint, "/")
		assertValue(call(endpoint, http.MethodGet, "alpha", ""), alpha.ID, "first")
		assertValue(call(endpoint, http.MethodGet, "beta", ""), beta.ID, "second")
		t.Logf("Durable Object namespace %s preserved separate SQLite values and rejected rolled-back writes after restarting container %s", namespace.ID, published.ContainerId)
	})
	if err := os.Chmod(configuration.Directory, 0o500); err != nil {
		t.Fatal(err)
	}
	storageFailure := valid
	storageFailure.Module += "\n// unique storage failure"
	storageFailure.Hostname = "storage.localhost"
	request(owner, http.MethodPost, deployPath(project.ID, "storage"), "storage", storageFailure, http.StatusInternalServerError)
	if err := os.Chmod(configuration.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	api.Close()
	closeSessions()
	public.Close()
	if err := publication.Close(); err != nil {
		t.Fatal(err)
	}
	front = newFront()
	publication, err = workerpublish.NewService(ctx, store, docker, newPublisher(front), configuration, "publication")
	if err != nil {
		t.Fatal(err)
	}
	handler, closeSessions = controlplane.NewHandlerWithWorkers(store, cfg, slog.Default(), publication)
	api = httptest.NewServer(handler)
	public = httptest.NewServer(front)
	getPublic()
	metadata = decode(request(owner, http.MethodGet, metadataPath(project.ID, initial.Id), "", nil, http.StatusOK))
	if metadata.ContainerId != initial.ContainerId || metadata.OperationOutcome != "succeeded" {
		t.Fatalf("restart lost deployment: %+v", metadata)
	}
	t.Logf("deployment=%s Worker=%s operation=%s digest=%s", initial.Id, initial.WorkerId, initial.OperationId, initial.ArtifactDigest)
	var operationCount int64
	if err := store.DB.Table("operations").Count(&operationCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.DB.Exec("SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype = 'advisory' AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())").Error; err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
lost:
	for {
		select {
		case <-deadline.C:
			t.Fatal("lost controller remained ready")
		case <-ticker.C:
			if err := publication.Ready(ctx); err != nil {
				break lost
			}
		}
	}
	request(owner, http.MethodGet, "/readyz", "", nil, http.StatusServiceUnavailable)
	request(owner, http.MethodPost, deployPath(project.ID, "lost-controller"), "lost-controller", valid, http.StatusInternalServerError)
	var countAfter int64
	if err := store.DB.Table("operations").Count(&countAfter).Error; err != nil {
		t.Fatal(err)
	}
	if countAfter != operationCount {
		t.Fatal("lost controller accepted a new operation")
	}
	getPublic()
	if err := publication.Close(); err == nil {
		t.Fatal("lost controller connection cleanup did not report its failure")
	}
	publication = nil
}
