//go:build integration

package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"gorm.io/gorm"
)

func TestPostgresProjectMemberRemoval(t *testing.T) {
	ctx := context.Background()
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

	accounts := []struct {
		email string
		pass  string
	}{
		{"owner-removal@cloudlet.local", "owner-removal-integration-password"},
		{"member-removal@cloudlet.local", "member-removal-integration-password"},
		{"outsider-removal@cloudlet.local", "outsider-removal-integration-password"},
	}
	users := make([]database.User, len(accounts))
	clients := make([]*http.Client, len(accounts))
	for i, account := range accounts {
		if err := store.SeedUser(ctx, account.email, account.pass); err != nil {
			t.Fatal(err)
		}
		users[i], err = store.Authenticate(ctx, account.email, account.pass)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = newSessionClient(t)
	}

	newServer := func() *httptest.Server {
		handler, cleanup, err := controlplane.NewHandler(store, config.Config{Instance: "member-removal", PublicOrigin: "http://localhost"}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	api := newServer()
	for i, account := range accounts {
		loginSession(t, api, clients[i], account.email, account.pass)
	}
	project := decodeProject(t, memberRequest(t, clients[0], http.MethodPost, api.URL+"/api/v1/projects", map[string]string{"name": "member removal"}, http.StatusCreated))
	if _, err := uuid.Parse(project.Id.String()); err != nil {
		t.Fatal(err)
	}
	membersURL := api.URL + "/api/v1/projects/" + project.Id.String() + "/members"
	added := decodeProjectMember(t, memberRequest(t, clients[0], http.MethodPost, membersURL, map[string]string{"email": accounts[1].email, "role": "developer"}, http.StatusOK))
	if added.UserId != users[1].ID {
		t.Fatalf("created membership for user %s, expected %s", added.UserId, users[1].ID)
	}
	memberURL := membersURL + "/" + users[1].ID
	memberRequest(t, newSessionClient(t), http.MethodDelete, memberURL, nil, http.StatusUnauthorized).Body.Close()
	memberRequest(t, clients[1], http.MethodDelete, memberURL, nil, http.StatusForbidden).Body.Close()
	memberRequest(t, clients[2], http.MethodDelete, memberURL, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, clients[0], http.MethodDelete, membersURL+"/"+users[0].ID, nil, http.StatusConflict).Body.Close()
	if err := store.RemoveProjectMember(ctx, project.Id.String(), users[0].ID); !errors.Is(err, database.ErrProjectOwner) {
		t.Fatalf("store allowed owner removal: %v", err)
	}
	memberRequest(t, clients[0], http.MethodDelete, memberURL, nil, http.StatusNoContent).Body.Close()
	memberRequest(t, clients[0], http.MethodDelete, memberURL, nil, http.StatusNotFound).Body.Close()

	members := decodeProjectMembers(t, memberRequest(t, clients[0], http.MethodGet, membersURL, nil, http.StatusOK))
	if len(members) != 1 || members[0].UserId != users[0].ID || members[0].Role != apigen.ProjectRoleOwner {
		t.Fatalf("membership list after removal: %+v", members)
	}
	memberRequest(t, clients[1], http.MethodGet, api.URL+"/api/v1/projects/"+project.Id.String(), nil, http.StatusNotFound).Body.Close()
	memberRequest(t, clients[1], http.MethodGet, membersURL, nil, http.StatusNotFound).Body.Close()
	if _, err := store.ProjectMember(ctx, project.Id.String(), users[1].ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("removed membership remains in database: %v", err)
	}

	api.Close()
	api = newServer()
	for i, account := range accounts {
		clients[i] = newSessionClient(t)
		loginSession(t, api, clients[i], account.email, account.pass)
	}
	membersURL = api.URL + "/api/v1/projects/" + project.Id.String() + "/members"
	members = decodeProjectMembers(t, memberRequest(t, clients[0], http.MethodGet, membersURL, nil, http.StatusOK))
	if len(members) != 1 || members[0].UserId != users[0].ID {
		t.Fatalf("membership list after API restart: %+v", members)
	}
	memberRequest(t, clients[1], http.MethodGet, api.URL+"/api/v1/projects/"+project.Id.String(), nil, http.StatusNotFound).Body.Close()
}
