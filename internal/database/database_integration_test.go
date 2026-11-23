//go:build integration

package database_test

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
	"github.com/wendaining/cloudlet/migrations"
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

	newServer := func(serverStore *database.Store) *httptest.Server {
		handler, cleanup, err := controlplane.NewHandler(serverStore, config.Config{Instance: "integration", PublicOrigin: "http://localhost"}, slog.Default())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cleanup)
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		return server
	}
	api := newServer(store)
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
			var actual apigen.Session
			if err := json.NewDecoder(response.Body).Decode(&actual); err != nil {
				t.Fatal(err)
			}
			if actual.User.Id != user.ID {
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
	api = newServer(store)
	request("GET", api.URL+"/api/v1/auth/session", "", http.StatusOK)
	request("GET", api.URL+"/api/v1/projects/"+projectID, "", http.StatusOK)
	member, err := store.ProjectMember(ctx, projectID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != "owner" || member.UserID != user.ID {
		t.Fatalf("项目所有者关系错误: %+v", member)
	}
	operationsInProject, err := store.ProjectOperations(ctx, user.ID, projectID)
	if err != nil || len(operationsInProject) != 0 {
		t.Fatalf("重启后项目内操作查询返回 %+v, %v", operationsInProject, err)
	}
	if visible, err := store.ProjectOperations(ctx, user.ID, uuid.NewString()); err != nil || len(visible) != 0 {
		t.Fatalf("未知项目操作查询返回 %+v, %v", visible, err)
	}
	if _, err := store.ProjectMember(ctx, projectID, otherUser.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("其他用户取得了项目成员关系: %v", err)
	}
	projectMemberLifecycle(t, ctx, api, store, client, project, user, otherUser, otherPassword)
	api.Close()
	reopened, err := database.Open(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	api = newServer(reopened)
	memberBase := api.URL + "/api/v1/projects/" + projectID + "/members"
	membersAfterRestart := decodeProjectMembers(t, memberRequest(t, client, http.MethodGet, memberBase, nil, http.StatusOK))
	if len(membersAfterRestart) != 3 || membersAfterRestart[0].Role != apigen.ProjectRoleOwner || membersAfterRestart[1].Role != apigen.ProjectRoleDeveloper || membersAfterRestart[2].Role != apigen.ProjectRoleViewer {
		t.Fatalf("服务重连后通过 HTTP 读取项目成员错误: %+v", membersAfterRestart)
	}
	persisted, err := reopened.ProjectMember(ctx, projectID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Role != "owner" {
		t.Fatalf("重新连接后项目所有者关系错误: %+v", persisted)
	}
	persistedMembers, err := reopened.ProjectMembers(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persistedMembers) != 3 || persistedMembers[0].Role != "owner" || persistedMembers[1].Role != "developer" || persistedMembers[2].Role != "viewer" {
		t.Fatalf("重新连接后项目成员错误: %+v", persistedMembers)
	}
	api.Close()
	if err := reopened.SQL.Close(); err != nil {
		t.Fatal(err)
	}
	api = newServer(store)
	t.Run("operations", func(t *testing.T) {
		testOperations(t, ctx, store, connection, project.Id, user.ID)
	})
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
	if err := store.DB.Exec("INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, TRUE)", migrations.Version+1).Error; err != nil {
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

func projectMemberLifecycle(t *testing.T, ctx context.Context, api *httptest.Server, store *database.Store, ownerClient *http.Client, project apigen.Project, owner, developer database.User, developerPassword string) {
	t.Helper()
	const viewerEmail = "observer@cloudlet.local"
	const viewerPassword = "observer-integration-password"
	const outsiderEmail = "outsider@cloudlet.local"
	const outsiderPassword = "outsider-integration-password"
	for _, account := range []struct {
		email    string
		password string
	}{
		{viewerEmail, viewerPassword},
		{outsiderEmail, outsiderPassword},
	} {
		if err := store.SeedUser(ctx, account.email, account.password); err != nil {
			t.Fatal(err)
		}
	}
	viewer, err := store.Authenticate(ctx, viewerEmail, viewerPassword)
	if err != nil {
		t.Fatal(err)
	}
	developerClient := newSessionClient(t)
	loginSession(t, api, developerClient, developer.Email, developerPassword)
	viewerClient := newSessionClient(t)
	loginSession(t, api, viewerClient, viewerEmail, viewerPassword)
	outsiderClient := newSessionClient(t)
	loginSession(t, api, outsiderClient, outsiderEmail, outsiderPassword)

	base := api.URL + "/api/v1/projects/" + project.Id.String() + "/members"
	members := decodeProjectMembers(t, memberRequest(t, ownerClient, http.MethodGet, base, nil, http.StatusOK))
	if len(members) != 1 || members[0].UserId != owner.ID || members[0].Email != owner.Email || members[0].Role != apigen.ProjectRole("owner") {
		t.Fatalf("初始项目成员错误: %+v", members)
	}

	added := decodeProjectMember(t, memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": developer.Email, "role": "developer"}, http.StatusOK))
	if added.UserId != developer.ID || added.Email != developer.Email || added.Role != apigen.ProjectRole("developer") {
		t.Fatalf("添加开发者返回错误: %+v", added)
	}
	members = decodeProjectMembers(t, memberRequest(t, ownerClient, http.MethodGet, base, nil, http.StatusOK))
	if len(members) != 2 || members[0].Role != apigen.ProjectRole("owner") || members[1].UserId != developer.ID || members[1].Role != apigen.ProjectRole("developer") {
		t.Fatalf("添加开发者后的成员列表错误: %+v", members)
	}

	projects := decodeProjects(t, memberRequest(t, developerClient, http.MethodGet, api.URL+"/api/v1/projects", nil, http.StatusOK))
	if len(projects) != 1 || projects[0].Id != project.Id {
		t.Fatalf("开发者读取项目列表错误: %+v", projects)
	}
	memberRequest(t, developerClient, http.MethodGet, api.URL+"/api/v1/projects/"+project.Id.String(), nil, http.StatusOK).Body.Close()
	memberRequest(t, developerClient, http.MethodGet, base, nil, http.StatusOK).Body.Close()
	memberRequest(t, developerClient, http.MethodPost, base, map[string]string{"email": viewerEmail, "role": "viewer"}, http.StatusForbidden).Body.Close()

	added = decodeProjectMember(t, memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": "  " + strings.ToUpper(viewerEmail) + "  ", "role": "viewer"}, http.StatusOK))
	if added.UserId != viewer.ID || added.Email != viewerEmail || added.Role != apigen.ProjectRole("viewer") {
		t.Fatalf("添加观察者返回错误: %+v", added)
	}
	projects = decodeProjects(t, memberRequest(t, viewerClient, http.MethodGet, api.URL+"/api/v1/projects", nil, http.StatusOK))
	if len(projects) != 1 || projects[0].Id != project.Id {
		t.Fatalf("观察者读取项目列表错误: %+v", projects)
	}
	memberRequest(t, viewerClient, http.MethodGet, api.URL+"/api/v1/projects/"+project.Id.String(), nil, http.StatusOK).Body.Close()
	memberRequest(t, viewerClient, http.MethodGet, base, nil, http.StatusOK).Body.Close()
	memberRequest(t, viewerClient, http.MethodPost, base, map[string]string{"email": developer.Email, "role": "viewer"}, http.StatusForbidden).Body.Close()

	updated := decodeProjectMember(t, memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": developer.Email, "role": "viewer"}, http.StatusOK))
	if updated.UserId != developer.ID || updated.Role != apigen.ProjectRole("viewer") {
		t.Fatalf("更新开发者角色错误: %+v", updated)
	}
	updated = decodeProjectMember(t, memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": developer.Email, "role": "developer"}, http.StatusOK))
	if updated.UserId != developer.ID || updated.Role != apigen.ProjectRole("developer") {
		t.Fatalf("恢复开发者角色错误: %+v", updated)
	}

	memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": owner.Email, "role": "developer"}, http.StatusConflict).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": "missing@cloudlet.local", "role": "developer"}, http.StatusNotFound).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": "not-an-email", "role": "developer"}, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]string{"email": viewerEmail, "role": "owner"}, http.StatusBadRequest).Body.Close()
	memberRequest(t, ownerClient, http.MethodPost, base, map[string]any{"email": viewerEmail, "role": "viewer", "typo": true}, http.StatusBadRequest).Body.Close()

	otherProject := decodeProject(t, memberRequest(t, outsiderClient, http.MethodPost, api.URL+"/api/v1/projects", map[string]string{"name": "其他项目"}, http.StatusCreated))
	otherBase := api.URL + "/api/v1/projects/" + otherProject.Id.String() + "/members"
	memberRequest(t, ownerClient, http.MethodPost, otherBase, map[string]string{"email": viewerEmail, "role": "viewer"}, http.StatusNotFound).Body.Close()
	memberRequest(t, developerClient, http.MethodGet, otherBase, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, viewerClient, http.MethodGet, otherBase, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, outsiderClient, http.MethodGet, base, nil, http.StatusNotFound).Body.Close()
	memberRequest(t, outsiderClient, http.MethodPost, base, map[string]string{"email": viewerEmail, "role": "viewer"}, http.StatusNotFound).Body.Close()

	storedMembers, err := store.ProjectMembers(ctx, project.Id.String())
	if err != nil {
		t.Fatal(err)
	}
	if len(storedMembers) != 3 || storedMembers[0].UserID != owner.ID || storedMembers[0].Role != "owner" || storedMembers[1].UserID != developer.ID || storedMembers[1].Role != "developer" || storedMembers[2].UserID != viewer.ID || storedMembers[2].Role != "viewer" {
		t.Fatalf("持久化的项目成员错误: %+v", storedMembers)
	}
	ownerMember, err := store.ProjectMember(ctx, project.Id.String(), owner.ID)
	if err != nil || ownerMember.Role != "owner" {
		t.Fatalf("项目所有者关系发生变化: %+v, %v", ownerMember, err)
	}
	if err := store.UpsertProjectMember(ctx, project.Id.String(), owner.ID, "viewer"); err == nil {
		t.Fatal("存储层允许修改项目所有者角色")
	}
	ownerMember, err = store.ProjectMember(ctx, project.Id.String(), owner.ID)
	if err != nil || ownerMember.Role != "owner" {
		t.Fatalf("存储层修改所有者后关系错误: %+v, %v", ownerMember, err)
	}

	longEmail := strings.Repeat("a", 168) + strings.Repeat("é", 40) + "@example.com"
	const longPassword = "long-email-integration-password"
	if err := store.SeedUser(ctx, longEmail, longPassword); err != nil {
		t.Fatal(err)
	}
	longUser, err := store.Authenticate(ctx, longEmail, longPassword)
	if err != nil {
		t.Fatal(err)
	}
	longClient := newSessionClient(t)
	loginSession(t, api, longClient, longEmail, longPassword)
	longProject := decodeProject(t, memberRequest(t, ownerClient, http.MethodPost, api.URL+"/api/v1/projects", map[string]string{"name": "长邮箱项目"}, http.StatusCreated))
	longBase := api.URL + "/api/v1/projects/" + longProject.Id.String() + "/members"
	added = decodeProjectMember(t, memberRequest(t, ownerClient, http.MethodPost, longBase, map[string]string{"email": longEmail, "role": "viewer"}, http.StatusOK))
	if added.UserId != longUser.ID || added.Email != strings.ToLower(longEmail) || added.Role != apigen.ProjectRole("viewer") {
		t.Fatalf("长邮箱账号加入项目错误: %+v", added)
	}
	memberRequest(t, longClient, http.MethodGet, api.URL+"/api/v1/projects/"+longProject.Id.String(), nil, http.StatusOK).Body.Close()
	tooLongEmail := strings.Repeat("a", 243) + "@example.com"
	if err := store.SeedUser(ctx, tooLongEmail, longPassword); err == nil {
		t.Fatal("超长邮箱通过了账号创建")
	}
	memberRequest(t, ownerClient, http.MethodPost, longBase, map[string]string{"email": tooLongEmail, "role": "viewer"}, http.StatusBadRequest).Body.Close()
}

func newSessionClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func loginSession(t *testing.T, api *httptest.Server, client *http.Client, email, password string) {
	t.Helper()
	response := memberRequest(t, client, http.MethodPost, api.URL+"/api/v1/auth/login", map[string]string{"email": email, "password": password}, http.StatusOK)
	response.Body.Close()
}

func memberRequest(t *testing.T, client *http.Client, method, endpoint string, body any, expected int) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != expected {
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("%s %s 返回 %d，要求 %d，响应 %s", method, endpoint, response.StatusCode, expected, data)
	}
	return response
}

func decodeProject(t *testing.T, response *http.Response) apigen.Project {
	t.Helper()
	defer response.Body.Close()
	var project apigen.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	return project
}

func decodeProjects(t *testing.T, response *http.Response) []apigen.Project {
	t.Helper()
	defer response.Body.Close()
	var projects []apigen.Project
	if err := json.NewDecoder(response.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	return projects
}

func decodeProjectMember(t *testing.T, response *http.Response) apigen.ProjectMember {
	t.Helper()
	defer response.Body.Close()
	var member apigen.ProjectMember
	if err := json.NewDecoder(response.Body).Decode(&member); err != nil {
		t.Fatal(err)
	}
	return member
}

func decodeProjectMembers(t *testing.T, response *http.Response) []apigen.ProjectMember {
	t.Helper()
	defer response.Body.Close()
	var members []apigen.ProjectMember
	if err := json.NewDecoder(response.Body).Decode(&members); err != nil {
		t.Fatal(err)
	}
	return members
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
	response = request(ownerClient, "GET", base+"/projects/"+project.Id.String()+"/operations", "", http.StatusOK)
	var operations []apigen.Operation
	if err := json.NewDecoder(response.Body).Decode(&operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 0 {
		t.Fatalf("新项目应没有操作记录: %+v", operations)
	}
	request(otherClient, "GET", base+"/projects/"+project.Id.String()+"/operations", "", http.StatusNotFound)
	request(ownerClient, "GET", base+"/projects/"+uuid.NewString()+"/operations", "", http.StatusNotFound)
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
