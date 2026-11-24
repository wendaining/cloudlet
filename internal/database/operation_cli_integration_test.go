package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/operations"
)

func testOperationCLI(t *testing.T, ctx context.Context, api *httptest.Server, store *database.Store, projectID uuid.UUID, otherOwnerID string, sessionClient *http.Client) {
	t.Helper()
	operationStore, err := operations.NewStore(store.DB)
	if err != nil {
		t.Fatal(err)
	}
	register := func(project uuid.UUID, key string) operations.Operation {
		t.Helper()
		op, created, err := operationStore.Register(ctx, operations.Registration{
			ProjectID: project, Action: "worker.deploy", IdempotencyKey: key, RequestFingerprint: key,
		})
		if err != nil || !created {
			t.Fatalf("register integration operation %+v, %t, %v", op, created, err)
		}
		return op
	}
	succeeded := register(projectID, "cli-success-"+uuid.NewString())
	if _, err := operationStore.Advance(ctx, projectID, succeeded.ID, operations.Received, operations.Progress{Phase: operations.Received, Outcome: operations.Succeeded}); err != nil {
		t.Fatal(err)
	}
	failed := register(projectID, "cli-failure-"+uuid.NewString())
	if _, err := operationStore.Advance(ctx, projectID, failed.ID, operations.Received, operations.Progress{Phase: operations.Received, Outcome: operations.Failed}); err != nil {
		t.Fatal(err)
	}
	pending := register(projectID, "cli-pending-"+uuid.NewString())

	otherProject, err := store.CreateProject(ctx, otherOwnerID, "CLI other account project")
	if err != nil {
		t.Fatal(err)
	}
	otherProjectID, err := uuid.Parse(otherProject.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherProjectOperation := register(otherProjectID, "cli-other-project-"+uuid.NewString())

	cookieParts := make([]string, 0)
	for _, cookie := range sessionClient.Jar.Cookies(mustURL(t, api.URL)) {
		cookieParts = append(cookieParts, cookie.Name+"="+cookie.Value)
	}
	if len(cookieParts) == 0 {
		t.Fatal("authenticated integration client has no session cookie")
	}
	cookie := strings.Join(cookieParts, "; ")

	binary := filepath.Join(t.TempDir(), "cloudlet")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/cloudlet")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}

	run := func(id, session string, fromEnvironment bool, expected int) string {
		t.Helper()
		args := []string{"operation", "get", "--endpoint", api.URL, "--project", projectID.String(), "--id", id, "--output", "json"}
		if session != "" && !fromEnvironment {
			args = append(args, "--cookie", session)
		}
		command := exec.CommandContext(ctx, binary, args...)
		if fromEnvironment {
			command.Env = append(os.Environ(), "CLOUDLET_COOKIE="+session)
		}
		stdout, err := command.Output()
		code := 0
		if err != nil {
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Fatalf("run CLI: %v", err)
			}
			code = exitError.ExitCode()
		}
		if code != expected {
			t.Fatalf("CLI operation %s exited %d, expected %d; output: %s", id, code, expected, stdout)
		}
		return string(stdout)
	}

	output := run(strings.ToUpper(succeeded.ID.String()), cookie, false, 0)
	var result struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil || result.ID != succeeded.ID.String() || result.Outcome != string(operations.Succeeded) {
		t.Fatalf("success CLI JSON result %+v, %v; raw %s", result, err, output)
	}
	run(failed.ID.String(), cookie, false, 2)
	run(pending.ID.String(), cookie, true, 3)
	run(succeeded.ID.String(), "", false, 4)
	run(otherProjectOperation.ID.String(), cookie, false, 5)
	run(uuid.NewString(), cookie, false, 5)
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
