//go:build integration

package database_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestCLIProjectsUsePostgresManagementAPI(t *testing.T) {
	ctx := context.Background()
	imageConfig, err := os.ReadFile(filepath.Join("..", "..", "configs", "images.json"))
	if err != nil {
		t.Fatal(err)
	}
	var images struct {
		Postgres string `json:"postgres"`
	}
	if err := json.Unmarshal(imageConfig, &images); err != nil {
		t.Fatal(err)
	}
	container, err := postgres.Run(ctx, images.Postgres,
		postgres.WithDatabase("cloudlet"), postgres.WithUsername("cloudlet"),
		postgres.WithPassword("integration-database-password"),
		testcontainers.WithCmd("postgres"), postgres.BasicWaitStrategies())
	if container != nil {
		t.Cleanup(func() {
			if err := container.Terminate(ctx); err != nil {
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
	t.Cleanup(func() { store.SQL.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const ownerEmail = "cli-owner@cloudlet.local"
	const ownerPassword = "integration-owner-password"
	const otherEmail = "cli-other@cloudlet.local"
	const otherPassword = "integration-other-password"
	if err := store.SeedUser(ctx, ownerEmail, ownerPassword); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, otherEmail, otherPassword); err != nil {
		t.Fatal(err)
	}
	handler, cleanup := controlplane.NewHandler(store, nil, config.Config{Instance: "cli-integration", PublicOrigin: "http://localhost"}, slog.Default())
	t.Cleanup(cleanup)
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	ownerSession := newSessionClient(t)
	loginSession(t, api, ownerSession, ownerEmail, ownerPassword)
	otherSession := newSessionClient(t)
	loginSession(t, api, otherSession, otherEmail, otherPassword)
	ownerProject := decodeProject(t, memberRequest(t, ownerSession, http.MethodPost, api.URL+"/api/v1/projects", map[string]string{"name": "owner-project"}, http.StatusCreated))
	otherProject := decodeProject(t, memberRequest(t, otherSession, http.MethodPost, api.URL+"/api/v1/projects", map[string]string{"name": "private-project"}, http.StatusCreated))

	var sessionCookie string
	apiURL, err := url.Parse(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range ownerSession.Jar.Cookies(apiURL) {
		if cookie.Name == "cloudlet_cli-integration_session" {
			sessionCookie = cookie.String()
			break
		}
	}
	if sessionCookie == "" {
		t.Fatal("login did not provide the expected session cookie")
	}

	binary := filepath.Join(t.TempDir(), "cloudlet")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/cloudlet")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cloudlet CLI: %v\n%s", err, output)
	}
	runCLI := func(cookie string, args ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = environmentWithoutSessionCookie()
		if cookie != "" {
			command.Env = append(command.Env, "CLOUDLET_COOKIE="+cookie)
		}
		return command.CombinedOutput()
	}

	listOutput, err := runCLI(sessionCookie, "projects", "list", "--endpoint", api.URL, "--output", "json")
	if err != nil {
		t.Fatalf("authenticated CLI project list failed: %v\n%s", err, listOutput)
	}
	var visible []apigen.Project
	if err := json.Unmarshal(listOutput, &visible); err != nil {
		t.Fatalf("decode CLI project list %q: %v", listOutput, err)
	}
	if len(visible) != 1 || visible[0].Id != ownerProject.Id || visible[0].Name != ownerProject.Name {
		t.Fatalf("CLI listed projects outside the authenticated account: %+v", visible)
	}
	getOutput, err := runCLI(sessionCookie, "projects", "get", ownerProject.Id.String(), "--endpoint", api.URL)
	if err != nil {
		t.Fatalf("authenticated CLI project detail failed: %v\n%s", err, getOutput)
	}
	if !strings.Contains(string(getOutput), ownerProject.Id.String()) || !strings.Contains(string(getOutput), ownerProject.Name) {
		t.Fatalf("CLI project detail output = %q", getOutput)
	}
	if unauthorizedOutput, err := runCLI("", "projects", "list", "--endpoint", api.URL); err == nil || !strings.Contains(string(unauthorizedOutput), "HTTP 401") {
		t.Fatalf("unauthenticated CLI request returned %v, output %q", err, unauthorizedOutput)
	}
	if deniedOutput, err := runCLI(sessionCookie, "projects", "get", otherProject.Id.String(), "--endpoint", api.URL); err == nil || !strings.Contains(string(deniedOutput), "HTTP 404") {
		t.Fatalf("cross-project CLI request returned %v, output %q", err, deniedOutput)
	}
}

func environmentWithoutSessionCookie() []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "CLOUDLET_COOKIE=") {
			environment = append(environment, value)
		}
	}
	return environment
}
