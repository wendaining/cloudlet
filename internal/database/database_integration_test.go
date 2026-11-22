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

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/internal/config"
	"github.com/wendaining/cloudlet/internal/controlplane"
	"github.com/wendaining/cloudlet/internal/database"
	"gorm.io/gorm"
)

func TestPostgresMigrationAccountsSessionsAndProjects(t *testing.T) {
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
	const otherEmail = "viewer@cloudlet.local"
	const otherPassword = "another-integration-password"
	if err := store.SeedUser(ctx, otherEmail, otherPassword); err != nil {
		t.Fatal(err)
	}
	otherUser, err := store.Authenticate(ctx, otherEmail, otherPassword)
	if err != nil {
		t.Fatal(err)
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
	project := projectLifecycle(t, api, client, user.ID, otherUser.ID)
	projectID := project.Id.String()
	request("POST", api.URL+"/api/v1/projects", `{}`, http.StatusBadRequest)
	request("POST", api.URL+"/api/v1/projects", `{"name":"   "}`, http.StatusBadRequest)
	request("POST", api.URL+"/api/v1/projects", `{"name":"ok","typo":true}`, http.StatusBadRequest)
	longName, err := json.Marshal(map[string]string{"name": strings.Repeat("a", 101)})
	if err != nil {
		t.Fatal(err)
	}
	request("POST", api.URL+"/api/v1/projects", string(longName), http.StatusBadRequest)
	boundaryName, err := json.Marshal(map[string]string{"name": "  " + strings.Repeat("项", 100) + "  "})
	if err != nil {
		t.Fatal(err)
	}
	request("POST", api.URL+"/api/v1/projects", string(boundaryName), http.StatusCreated)
	if _, err := store.CreateProject(ctx, uuid.NewString(), "事务失败"); err == nil {
		t.Fatal("未知所有者创建项目时未失败")
	}
	var projectCount int64
	if err := store.DB.Model(&database.Project{}).Where("name = ?", "事务失败").Count(&projectCount).Error; err != nil {
		t.Fatal(err)
	}
	if projectCount != 0 {
		t.Fatal("所有者写入失败后残留了项目")
	}
	api.Close()
	api = newServer()
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusOK)
	request("GET", api.URL+"/api/v1/projects/"+projectID, "", http.StatusOK)
	member, err := store.ProjectMember(ctx, projectID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != "owner" || member.UserID != user.ID {
		t.Fatalf("项目所有者关系错误: %+v", member)
	}
	if _, err := store.ProjectMember(ctx, projectID, otherUser.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("其他用户取得了项目成员关系: %v", err)
	}
	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.ProjectMember(ctx, projectID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Role != "owner" {
		t.Fatalf("重新连接后项目所有者关系错误: %+v", persisted)
	}
	if err := reopened.SQL.Close(); err != nil {
		t.Fatal(err)
	}
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
	if err := store.DB.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (3, TRUE)").Error; err != nil {
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

func projectLifecycle(t *testing.T, api *httptest.Server, ownerClient *http.Client, ownerID, otherUserID string) apigen.Project {
	t.Helper()
	newClient := func() *http.Client {
		jar, err := cookiejar.New(nil)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Client{Jar: jar}
	}
	otherClient := newClient()
	request := func(client *http.Client, method, endpoint, body string, expected int) *http.Response {
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
		t.Cleanup(func() { response.Body.Close() })
		if response.StatusCode != expected {
			t.Fatalf("%s %s 返回 %d，要求 %d", method, endpoint, response.StatusCode, expected)
		}
		return response
	}
	base := api.URL + "/api/v1"
	request(newClient(), "GET", base+"/projects", "", http.StatusUnauthorized)
	request(otherClient, "POST", base+"/auth/login", `{"email":"viewer@cloudlet.local","password":"another-integration-password"}`, http.StatusOK)
	response := request(ownerClient, "POST", base+"/projects", `{"name":"  示例项目  "}`, http.StatusCreated)
	var project apigen.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.Id == uuid.Nil || project.Name != "示例项目" {
		t.Fatalf("创建项目返回错误: %+v", project)
	}
	response = request(ownerClient, "GET", base+"/projects", "", http.StatusOK)
	var projects []apigen.Project
	if err := json.NewDecoder(response.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Id != project.Id || projects[0].Name != project.Name {
		t.Fatalf("项目列表错误: %+v", projects)
	}
	response = request(ownerClient, "GET", base+"/projects/"+project.Id.String(), "", http.StatusOK)
	var fetched apigen.Project
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched != project {
		t.Fatalf("读取项目返回错误: %+v", fetched)
	}
	response = request(ownerClient, "GET", base+"/projects/urn:uuid:"+project.Id.String(), "", http.StatusOK)
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched != project {
		t.Fatalf("UUID URN 读取项目返回错误: %+v", fetched)
	}
	response = request(ownerClient, "GET", base+"/projects/"+strings.ReplaceAll(project.Id.String(), "-", ""), "", http.StatusOK)
	if err := json.NewDecoder(response.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	if fetched != project {
		t.Fatalf("无连字符 UUID 读取项目返回错误: %+v", fetched)
	}
	response = request(otherClient, "GET", base+"/projects", "", http.StatusOK)
	if err := json.NewDecoder(response.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("其他用户读取到项目列表: %+v", projects)
	}
	request(otherClient, "GET", base+"/projects/"+project.Id.String(), "", http.StatusNotFound)
	request(otherClient, "GET", base+"/projects/urn:uuid:"+project.Id.String(), "", http.StatusNotFound)
	request(otherClient, "GET", base+"/projects/not-a-project", "", http.StatusNotFound)
	if ownerID == otherUserID {
		t.Fatal("测试账号必须不同")
	}
	return project
}
