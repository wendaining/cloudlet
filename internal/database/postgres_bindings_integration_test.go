//go:build integration

package database_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
)

func TestPostgresBindingHTTP(t *testing.T) {
	ctx := t.Context()
	store, _, _ := openIntegrationDatabase(t, ctx)
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	business, dsn, _ := openIntegrationDatabase(t, ctx)
	if _, err := business.SQL.ExecContext(ctx, `CREATE ROLE old_app LOGIN PASSWORD 'expired-test-password' VALID UNTIL '2000-01-01'`); err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	const password = "integration-account-password"
	emails := []string{"db-owner@cloudlet.local", "db-other@cloudlet.local"}
	owner, other := makeProjectOwners(t, store, emails, password)
	key := make([]byte, 32)
	createSecret := func(project, name, value string) uuid.UUID {
		t.Helper()
		version, err := store.CreateProjectSecretVersion(ctx, project, name, value, key)
		if err != nil {
			t.Fatal(err)
		}
		return uuid.MustParse(version.ID)
	}
	valid := createSecret(owner.ID, "business", `{"username":"cloudlet","password":"integration-database-password"}`)
	expired := createSecret(owner.ID, "expired", `{"username":"old_app","password":"expired-test-password"}`)
	foreign := createSecret(other.ID, "business", `{"username":"foreign","password":"foreign-secret"}`)

	handler, cleanup := controlplane.NewHandler(store, config.Config{Instance: "integration", Environment: "development", PublicOrigin: "http://localhost", SecretKey: key}, slog.Default())
	t.Cleanup(cleanup)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ownerHTTP := newSessionClient(t)
	otherHTTP := newSessionClient(t)
	authenticatedAPI(t, server.URL, ownerHTTP, emails[0], password)
	authenticatedAPI(t, server.URL, otherHTTP, emails[1], password)

	root := server.URL + "/api/v1/projects/" + owner.ID + "/postgres-bindings"
	body := func(response *http.Response) string {
		t.Helper()
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"integration-database-password", "expired-test-password", "foreign-secret"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("response contains a secret")
			}
		}
		return string(data)
	}
	input := apigen.PostgresBindingInput{Name: "orders", Host: endpoint.Hostname(), Port: port, Database: "cloudlet", SecretVersionId: valid}
	createdResponse := memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusCreated)
	if createdResponse.Header.Get("Location") != "/api/v1/projects/"+owner.ID+"/postgres-bindings/orders" {
		t.Fatal("incorrect binding location")
	}
	var created, read apigen.PostgresBinding
	if err := json.Unmarshal([]byte(body(createdResponse)), &created); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/orders", nil, http.StatusOK))), &read); err != nil {
		t.Fatal(err)
	}
	if created.Name != input.Name || created.Host != input.Host || created.Port != input.Port ||
		created.Database != input.Database || created.SecretVersionId != valid || created.VerifiedAt.IsZero() || created != read {
		t.Fatalf("unexpected binding: created=%+v read=%+v", created, read)
	}
	body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusConflict))
	body(memberRequest(t, otherHTTP, http.MethodGet, root+"/orders", nil, http.StatusNotFound))

	input.Name, input.SecretVersionId = "expired", expired
	failed := body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusUnprocessableEntity))
	if !strings.Contains(failed, "身份验证失败") {
		t.Fatalf("expected authentication diagnostic, got %s", failed)
	}
	body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/expired", nil, http.StatusNotFound))

	input.Name, input.SecretVersionId = "foreign", foreign
	body(memberRequest(t, ownerHTTP, http.MethodPost, root, input, http.StatusNotFound))
	body(memberRequest(t, ownerHTTP, http.MethodGet, root+"/foreign", nil, http.StatusNotFound))
}
