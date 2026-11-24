//go:build integration

package database_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"github.com/wendaining/cloudlet/internal/server"
	"gorm.io/gorm"
)

func TestProjectSecretsEncryptAtRestAndExposeMetadataOnly(t *testing.T) {
	ctx := context.Background()
	store, connection := openSecretTestStore(t, ctx)
	owner, developer, viewer, outsider := seedSecretUsers(t, ctx, store)
	project, err := store.CreateProject(ctx, owner.ID, "凭据项目")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []struct {
		user database.User
		role string
	}{
		{developer, "developer"},
		{viewer, "viewer"},
	} {
		if err := store.UpsertProjectMember(ctx, project.ID, member.user.ID, member.role); err != nil {
			t.Fatal(err)
		}
	}
	otherProject, err := store.CreateProject(ctx, outsider.ID, "其他项目")
	if err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	handler, cleanup := controlplane.NewHandler(store, config.Config{
		Instance:     "integration",
		PublicOrigin: "http://localhost",
		SecretKey:    key,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(cleanup)
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)

	ownerClient := newSessionClient(t)
	developerClient := newSessionClient(t)
	viewerClient := newSessionClient(t)
	outsiderClient := newSessionClient(t)
	loginSession(t, api, ownerClient, owner.Email, "secret-owner-password")
	loginSession(t, api, developerClient, developer.Email, "secret-developer-password")
	loginSession(t, api, viewerClient, viewer.Email, "secret-viewer-password")
	loginSession(t, api, outsiderClient, outsider.Email, "secret-outsider-password")

	base := api.URL + "/api/v1/projects/" + project.ID + "/secrets"
	const firstName = "DATABASE_URL"
	firstValue := "postgres://owner:first-password@database:5432/app"
	first := createSecret(t, ownerClient, base, firstName, firstValue)
	if first.Name != firstName || first.Version != 1 || first.Id == uuid.Nil || first.SecretId == uuid.Nil {
		t.Fatalf("first secret version = %+v", first)
	}
	secondValue := "postgres://owner:second-password@database:5432/app"
	second := createSecret(t, ownerClient, base, firstName, secondValue)
	if second.SecretId != first.SecretId || second.Version != 2 {
		t.Fatalf("second secret version = %+v", second)
	}

	secretsBody := readBody(t, memberRequest(t, ownerClient, http.MethodGet, base, nil, http.StatusOK))
	if strings.Contains(secretsBody, "first-password") || strings.Contains(secretsBody, "second-password") || strings.Contains(secretsBody, `"value"`) {
		t.Fatalf("metadata list contains secret material: %s", secretsBody)
	}
	var secrets []apigen.ProjectSecret
	if err := json.Unmarshal([]byte(secretsBody), &secrets); err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Name != firstName || secrets[0].LatestVersion != 2 || secrets[0].Id != first.SecretId {
		t.Fatalf("secret metadata = %+v", secrets)
	}

	versionsBody := readBody(t, memberRequest(t, developerClient, http.MethodGet, base+"/"+firstName, nil, http.StatusOK))
	if strings.Contains(versionsBody, "first-password") || strings.Contains(versionsBody, "second-password") || strings.Contains(versionsBody, `"value"`) {
		t.Fatalf("version metadata contains secret material: %s", versionsBody)
	}
	var versions []apigen.ProjectSecretVersion
	if err := json.Unmarshal([]byte(versionsBody), &versions); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 || versions[0].Id != second.Id || versions[1].Id != first.Id {
		t.Fatalf("secret versions = %+v", versions)
	}

	memberRequest(t, viewerClient, http.MethodGet, base, nil, http.StatusOK).Body.Close()
	memberRequest(t, viewerClient, http.MethodGet, base+"/"+firstName, nil, http.StatusOK).Body.Close()
	valuePath := base + "/" + firstName + "/versions/1/value"
	ownerValue := getSecretValue(t, ownerClient, valuePath)
	if ownerValue.Value != firstValue || ownerValue.Version != 1 || ownerValue.Name != firstName {
		t.Fatalf("owner read value = %+v", ownerValue)
	}
	memberRequest(t, developerClient, http.MethodPost, base, map[string]string{"name": "DENIED", "value": "denied"}, http.StatusForbidden).Body.Close()
	memberRequest(t, viewerClient, http.MethodPost, base, map[string]string{"name": "DENIED", "value": "denied"}, http.StatusForbidden).Body.Close()
	memberRequest(t, developerClient, http.MethodGet, valuePath, nil, http.StatusForbidden).Body.Close()
	memberRequest(t, viewerClient, http.MethodGet, valuePath, nil, http.StatusForbidden).Body.Close()
	memberRequest(t, outsiderClient, http.MethodGet, base, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, outsiderClient, http.MethodGet, base+"/"+firstName, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, outsiderClient, http.MethodGet, valuePath, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerClient, http.MethodGet, base+"/"+firstName+"/versions/3/value", nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerClient, http.MethodGet, api.URL+"/api/v1/projects/"+otherProject.ID+"/secrets", nil, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]any{"name": "BAD-NAME", "value": "value"}, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]any{"name": "EMPTY", "value": ""}, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]any{"name": "UNKNOWN", "value": "value", "typo": true}, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerClient, http.MethodGet, base+"/BAD-NAME", nil, http.StatusNotFound).Body.Close()
	t.Run("missing and foreign credentials", func(t *testing.T) {
		otherBase := api.URL + "/api/v1/projects/" + otherProject.ID + "/secrets"
		createSecret(t, outsiderClient, otherBase, "ONLY_OTHER", "other-project-value")
		for _, name := range []string{"MISSING", "ONLY_OTHER"} {
			memberRequest(t, ownerClient, http.MethodGet, base+"/"+name, nil, http.StatusNotFound).Body.Close()
			memberRequest(t, ownerClient, http.MethodGet, base+"/"+name+"/versions/1/value", nil, http.StatusNotFound).Body.Close()
		}
		memberRequest(t, outsiderClient, http.MethodGet, otherBase+"/"+firstName+"/versions/1/value", nil, http.StatusNotFound).Body.Close()
	})
	t.Run("decoded UTF-8 size boundaries", func(t *testing.T) {
		for _, tt := range []struct{ name, value string }{
			{"ASCII_BOUNDARY", strings.Repeat("a", 65536)},
			{"QUOTES_BOUNDARY", strings.Repeat("\"", 65536)},
			{"ESCAPED_BOUNDARY", strings.Repeat("<", 65536)},
			{"UTF8_BOUNDARY", strings.Repeat("汉", 21845) + "a"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				created := createSecret(t, ownerClient, base, tt.name, tt.value)
				value := getSecretValue(t, ownerClient, base+"/"+tt.name+"/versions/1/value")
				if created.Version != 1 || value.Value != tt.value {
					t.Fatal("boundary value did not round trip")
				}
			})
		}
		for _, value := range []string{strings.Repeat("a", 65537), strings.Repeat("汉", 21846)} {
			memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"name": "OVERSIZE", "value": value}, http.StatusBadRequest).Body.Close()
		}
	})
	t.Run("canonical project identity", func(t *testing.T) {
		for index, id := range []string{strings.ToUpper(project.ID), "urn:uuid:" + project.ID} {
			name := fmt.Sprintf("CANONICAL_%d", index)
			version, err := store.CreateProjectSecretVersion(ctx, id, name, "canonical-value", key)
			if err != nil {
				t.Fatal(err)
			}
			for _, readID := range []string{project.ID, id} {
				value, err := store.ProjectSecretValueByName(ctx, readID, name, version.Version, key)
				if err != nil || value.Value != "canonical-value" {
					t.Fatalf("canonical read: %v", err)
				}
			}
		}
	})
	t.Run("ciphertext is bound to its version", func(t *testing.T) {
		first, err := store.CreateProjectSecretVersion(ctx, project.ID, "SWAP", "first-value", key)
		if err != nil {
			t.Fatal(err)
		}
		second, err := store.CreateProjectSecretVersion(ctx, project.ID, "SWAP", "second-value", key)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.DB.Exec(`UPDATE project_secret_versions SET (nonce, ciphertext) = (SELECT nonce, ciphertext FROM project_secret_versions WHERE id = ?) WHERE id = ?`, first.ID, second.ID).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := store.ProjectSecretValueByName(ctx, project.ID, "SWAP", 2, key); err == nil {
			t.Fatal("another version's ciphertext was accepted")
		}
	})
	t.Run("wrong platform key rejects writes and startup", func(t *testing.T) {
		wrongKey := bytes.Repeat([]byte{1}, 32)
		for _, name := range []string{firstName, "WRONG_KEY"} {
			if _, err := store.CreateProjectSecretVersion(ctx, project.ID, name, "wrong-key-value", wrongKey); err == nil {
				t.Fatal("wrong key appended a credential version")
			}
		}
		versions, err := store.ProjectSecretVersions(ctx, project.ID, firstName)
		if err != nil || len(versions) != 2 {
			t.Fatalf("wrong key changed versions: count=%d error=%v", len(versions), err)
		}
		versions, err = store.ProjectSecretVersions(ctx, project.ID, "WRONG_KEY")
		if err != nil || len(versions) != 0 {
			t.Fatalf("wrong key created a credential: count=%d error=%v", len(versions), err)
		}
		startupCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		err = server.Run(startupCtx, config.Config{DatabaseURL: connection, SecretKey: wrongKey, APIAddress: "127.0.0.1:0", GatewayAddress: "127.0.0.1:0"}, slog.Default())
		if err == nil || !strings.Contains(err.Error(), "platform secret key") {
			t.Fatalf("wrong key startup error = %v", err)
		}
	})

	var ciphertext []byte
	if err := store.DB.Raw(`SELECT ciphertext FROM project_secret_versions WHERE id = ?`, first.Id.String()).Row().Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(firstValue)) || bytes.Contains(ciphertext, []byte(secondValue)) {
		t.Fatal("database ciphertext contains plaintext")
	}
	if strings.Contains(logs.String(), "first-password") || strings.Contains(logs.String(), "second-password") {
		t.Fatalf("logs contain secret material: %s", logs.String())
	}

	api.Close()
	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.SQL.Close() })
	restartedHandler, restartedCleanup := controlplane.NewHandler(reopened, config.Config{
		Instance:     "integration",
		PublicOrigin: "http://localhost",
		SecretKey:    key,
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(restartedCleanup)
	restarted := httptest.NewServer(restartedHandler)
	t.Cleanup(restarted.Close)
	restartedValue := getSecretValue(t, ownerClient, restarted.URL+"/api/v1/projects/"+project.ID+"/secrets/"+firstName+"/versions/2/value")
	if restartedValue.Value != secondValue || restartedValue.Version != 2 {
		t.Fatalf("value after restart = %+v", restartedValue)
	}
	value, err := reopened.ProjectSecretValue(ctx, project.ID, first.Id.String(), key)
	if err != nil {
		t.Fatal(err)
	}
	if value.Value != firstValue || value.Version != 1 || value.Name != firstName {
		t.Fatalf("decrypted secret = %+v", value)
	}
	if _, err := reopened.ProjectSecretValue(ctx, project.ID, first.Id.String(), bytes.Repeat([]byte{1}, 32)); !errors.Is(err, database.ErrSecretKeyMismatch) {
		t.Fatalf("wrong key read error = %v, want key mismatch", err)
	}
	if _, err := reopened.ProjectSecretValue(ctx, otherProject.ID, first.Id.String(), key); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-project read error = %v, want record not found", err)
	}

	const concurrent = 8
	type result struct {
		version int
		err     error
	}
	results := make(chan result, concurrent)
	var group sync.WaitGroup
	for range concurrent {
		group.Add(1)
		go func() {
			defer group.Done()
			version, err := reopened.CreateProjectSecretVersion(ctx, project.ID, "CONCURRENT", "value", key)
			results <- result{version: version.Version, err: err}
		}()
	}
	group.Wait()
	close(results)
	seen := make(map[int]bool, concurrent)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if seen[result.version] {
			t.Fatalf("duplicate concurrent version %d", result.version)
		}
		seen[result.version] = true
	}
	for version := 1; version <= concurrent; version++ {
		if !seen[version] {
			t.Fatalf("missing concurrent version %d in %v", version, seen)
		}
	}
}

func openSecretTestStore(t *testing.T, ctx context.Context) (*database.Store, string) {
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
		postgres.WithPassword("secret-integration-password"),
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
	t.Cleanup(func() { _ = store.SQL.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return store, connection
}

func seedSecretUsers(t *testing.T, ctx context.Context, store *database.Store) (database.User, database.User, database.User, database.User) {
	t.Helper()
	accounts := []struct {
		email    string
		password string
	}{
		{"secret-owner@cloudlet.local", "secret-owner-password"},
		{"secret-developer@cloudlet.local", "secret-developer-password"},
		{"secret-viewer@cloudlet.local", "secret-viewer-password"},
		{"secret-outsider@cloudlet.local", "secret-outsider-password"},
	}
	users := make([]database.User, 0, len(accounts))
	for _, account := range accounts {
		if err := store.SeedUser(ctx, account.email, account.password); err != nil {
			t.Fatal(err)
		}
		user, err := store.Authenticate(ctx, account.email, account.password)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, user)
	}
	return users[0], users[1], users[2], users[3]
}

func createSecret(t *testing.T, client *http.Client, base, name, value string) apigen.ProjectSecretVersion {
	t.Helper()
	response := memberRequest(t, client, http.MethodPost, base, map[string]string{"name": name, "value": value}, http.StatusCreated)
	body := readBody(t, response)
	if strings.Contains(body, value) || strings.Contains(body, `"value"`) {
		t.Fatalf("create response contains secret material: %s", body)
	}
	var version apigen.ProjectSecretVersion
	if err := json.Unmarshal([]byte(body), &version); err != nil {
		t.Fatal(err)
	}
	return version
}

func getSecretValue(t *testing.T, client *http.Client, endpoint string) apigen.ProjectSecretValue {
	t.Helper()
	response := memberRequest(t, client, http.MethodGet, endpoint, nil, http.StatusOK)
	defer response.Body.Close()
	var value apigen.ProjectSecretValue
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
