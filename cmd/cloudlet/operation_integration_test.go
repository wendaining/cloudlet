//go:build integration

package main

import (
	"encoding/json"
	"errors"
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

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
)

func TestOperationWaitWithManagementAPI(t *testing.T) {
	ctx := t.Context()
	data, err := os.ReadFile("../../configs/images.json")
	if err != nil {
		t.Fatal(err)
	}
	var images struct{ Postgres string }
	if err := json.Unmarshal(data, &images); err != nil {
		t.Fatal(err)
	}

	container, err := postgres.Run(ctx, images.Postgres, postgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, container)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.SQL.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	const email = "cli-wait@cloudlet.local"
	const password = "cli-wait-integration-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	account, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.CreateProject(ctx, account.ID, "CLI operation wait")
	if err != nil {
		t.Fatal(err)
	}
	projectID := uuid.MustParse(project.ID)
	operationStore, err := operations.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "cloudlet")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("compile CLI: %v\n%s", err, output)
	}

	handler, cleanup := controlplane.NewHandler(store, config.Config{Instance: "cli-wait", PublicOrigin: "http://localhost"}, slog.Default())
	defer cleanup()
	for _, test := range []struct {
		name    string
		outcome operations.Outcome
		timeout string
		code    int
	}{
		{"success", operations.Succeeded, "10s", 0},
		{"failure", operations.Failed, "10s", 2},
		{"deadline", "", "500ms", 124},
		{"cancel", "", "10s", 130},
	} {
		t.Run(test.name, func(t *testing.T) {
			record, _, err := operationStore.Register(ctx, operations.Registration{
				ProjectID: projectID, Action: "deploy", IdempotencyKey: test.name, RequestFingerprint: "cli-wait-integration",
			})
			if err != nil {
				t.Fatal(err)
			}
			firstQuery := make(chan struct{})
			var first sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r)
				if r.Method == http.MethodGet && r.URL.Path == "/api/v1/projects/"+project.ID+"/operations" {
					first.Do(func() { close(firstQuery) })
				}
			}))
			defer server.Close()

			login, err := http.Post(server.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"email":"`+email+`","password":"`+password+`"}`))
			if err != nil {
				t.Fatal(err)
			}
			login.Body.Close()
			cookies := login.Cookies()
			if login.StatusCode != http.StatusOK || len(cookies) != 1 {
				t.Fatalf("login returned %d and %d cookies", login.StatusCode, len(cookies))
			}

			cmd := exec.CommandContext(ctx, binary, "operation", "wait", "--endpoint", server.URL, "--project", project.ID,
				"--id", record.ID.String(), "--timeout", test.timeout, "--output", "json")
			cmd.Env = append(os.Environ(), "CLOUDLET_COOKIE="+cookies[0].Name+"="+cookies[0].Value)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			select {
			case <-firstQuery:
			case <-time.After(5 * time.Second):
				t.Fatal("CLI did not query the management API")
			}
			if test.outcome != "" {
				if _, err := operationStore.Advance(ctx, projectID, record.ID, operations.Received,
					operations.Progress{Phase: operations.Received, Outcome: test.outcome}); err != nil {
					t.Fatal(err)
				}
			}
			if test.name == "cancel" {
				if err := cmd.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
			}
			code := 0
			if err := cmd.Wait(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != test.code {
				t.Fatalf("exit = %d, want %d: %s", code, test.code, stderr.String())
			}
			if test.outcome != "" {
				var result struct {
					ID      uuid.UUID
					Outcome operations.Outcome
				}
				if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
					t.Fatal(err)
				}
				if result.ID != record.ID || result.Outcome != test.outcome {
					t.Fatalf("unexpected terminal result: %+v", result)
				}
			} else if stdout.Len() != 0 || stderr.Len() == 0 {
				t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
			}
		})
	}
}
