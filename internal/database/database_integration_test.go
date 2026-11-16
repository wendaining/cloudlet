//go:build integration

package database_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
)

func TestPostgresMigrationAccountsAndSessions(t *testing.T) {
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
	if err := store.CheckVersion(ctx); err == nil {
		t.Fatal("未迁移的数据库通过了版本检查")
	}
	for range 2 {
		if err := store.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	const email = "developer@cloudlet.local"
	const password = "integration-account-password"
	if err := store.SeedUser(ctx, email, password); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedUser(ctx, email, "another-account-password"); err != nil {
		t.Fatal(err)
	}
	user, err := store.Authenticate(ctx, email, password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Authenticate(ctx, email, "wrong"); !errors.Is(err, database.ErrCredentials) {
		t.Fatalf("错误密码返回 %v", err)
	}

	newServer := func() *httptest.Server {
		handler, cleanup := controlplane.NewHandler(store, config.Config{Instance: "integration", PublicOrigin: "http://localhost"}, slog.Default())
		t.Cleanup(cleanup)
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	api := newServer()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	request := func(method, endpoint, body string, expected int) {
		t.Helper()
		req, err := http.NewRequest(method, endpoint, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != expected {
			t.Fatalf("%s %s 返回 %d，要求 %d", method, endpoint, response.StatusCode, expected)
		}
		if expected == http.StatusOK && strings.HasSuffix(endpoint, "/auth/session") {
			var actual database.User
			if err := json.NewDecoder(response.Body).Decode(&actual); err != nil {
				t.Fatal(err)
			}
			if actual.ID != user.ID {
				t.Fatalf("会话账号发生变化: %+v", actual)
			}
		}
	}
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusUnauthorized)
	request("POST", api.URL+"/api/v1/auth/login", `{"email":"developer@cloudlet.local","password":"wrong"}`, http.StatusUnauthorized)
	request("POST", api.URL+"/api/v1/auth/login", `{"email":"developer@cloudlet.local","password":"integration-account-password","typo":1}`, http.StatusBadRequest)
	request("POST", api.URL+"/api/v1/auth/login", `{"email":"developer@cloudlet.local","password":"integration-account-password"}`, http.StatusOK)
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusOK)
	request("GET", api.URL+"/api/v1/environment", "", http.StatusOK)
	api.Close()
	api = newServer()
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusOK)
	request("POST", api.URL+"/api/v1/auth/logout", "", http.StatusNoContent)
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusUnauthorized)
	req, err := http.NewRequest("POST", api.URL+"/api/v1/auth/login", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://outside.example")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("跨来源请求未被拒绝")
	}
	if err := store.DB.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (2, TRUE)").Error; err != nil {
		t.Fatal(err)
	}
	if err := store.CheckVersion(ctx); err == nil {
		t.Fatal("数据库版本超出代码版本时未返回错误")
	}
	if err := store.Migrate(ctx); err == nil {
		t.Fatal("迁移入口接受了超出代码版本的数据库")
	}
	if err := container.Stop(ctx, nil); err != nil {
		t.Fatal(err)
	}
	request("GET", api.URL+"/readyz", "", http.StatusServiceUnavailable)
}
