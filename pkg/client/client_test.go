package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewRejectsInvalidEndpoints(t *testing.T) {
	tests := []string{
		"",
		"127.0.0.1:8080",
		"ftp://127.0.0.1:8080",
		"http://user@127.0.0.1:8080",
		"http://127.0.0.1:8080?debug=1",
		"http://127.0.0.1:8080/#fragment",
		"http://127.0.0.1:8080/api",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := New(endpoint); err == nil {
				t.Fatalf("New(%q) succeeded", endpoint)
			}
		})
	}
}

func TestSetSessionCookieTrimsAndRejectsLineBreaks(t *testing.T) {
	api, err := New("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("  cloudlet_dev_session=value \t"); err != nil {
		t.Fatal(err)
	}
	if api.cookie != "cloudlet_dev_session=value" {
		t.Fatalf("cookie = %q", api.cookie)
	}
	for _, cookie := range []string{"a\rb", "a\nb"} {
		if err := api.SetSessionCookie(cookie); err == nil {
			t.Fatalf("SetSessionCookie(%q) succeeded", cookie)
		}
	}
}

func TestAPIError(t *testing.T) {
	err := (&APIError{StatusCode: http.StatusNotFound}).Error()
	if err != "management API returned HTTP 404" {
		t.Fatalf("APIError.Error() = %q", err)
	}
}

func TestManagementClientQueriesProjectsAndOperations(t *testing.T) {
	projectID := uuid.New()
	operationID := uuid.New()
	createdAt := time.Date(2026, 11, 23, 12, 34, 56, 123456789, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "cloudlet_dev_session=value" {
			t.Errorf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`[{"id":"` + projectID.String() + `","name":"alpha"}]`))
		case "/api/v1/projects/" + projectID.String():
			_, _ = w.Write([]byte(`{"id":"` + projectID.String() + `","name":"alpha"}`))
		case "/api/v1/projects/" + projectID.String() + "/operations":
			_, _ = w.Write([]byte(`[{"id":"` + operationID.String() + `","action":"deploy","phase":"preparing","outcome":"succeeded","created_at":"` +
				createdAt.Format(time.RFC3339Nano) + `","updated_at":"` + createdAt.Add(time.Second).Format(time.RFC3339Nano) + `"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("cloudlet_dev_session=value"); err != nil {
		t.Fatal(err)
	}
	projects, err := api.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Id != projectID || projects[0].Name != "alpha" {
		t.Fatalf("ListProjects() = %#v", projects)
	}
	project, err := api.GetProject(context.Background(), projectID.String())
	if err != nil {
		t.Fatal(err)
	}
	if project.Id != projectID || project.Name != "alpha" {
		t.Fatalf("GetProject() = %#v", project)
	}
	operation, err := api.ProjectOperation(context.Background(), strings.ToUpper(projectID.String()), strings.ToUpper(operationID.String()))
	if err != nil {
		t.Fatal(err)
	}
	if operation.Id != operationID || operation.Action != "deploy" || operation.CreatedAt != createdAt {
		t.Fatalf("ProjectOperation() = %#v", operation)
	}
}

func TestManagementClientReportsResponseErrors(t *testing.T) {
	projectID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := http.StatusInternalServerError
		if strings.HasSuffix(r.URL.Path, "/operations") {
			status = http.StatusInternalServerError
		} else if r.URL.Path == "/api/v1/projects" {
			status = http.StatusUnauthorized
		} else {
			status = http.StatusNotFound
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"request rejected"}`))
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ListProjects(context.Background()); err == nil || !strings.Contains(err.Error(), "request rejected") {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if _, err := api.GetProject(context.Background(), projectID.String()); err == nil || !strings.Contains(err.Error(), "request rejected") {
		t.Fatalf("GetProject() error = %v", err)
	}
	if _, err := api.ListProjectOperations(context.Background(), projectID.String()); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("ListProjectOperations() error = %v", err)
	}
}

func TestProjectOperationErrors(t *testing.T) {
	projectID := uuid.New()
	operationID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ProjectOperation(context.Background(), "invalid", operationID.String()); err == nil {
		t.Fatal("ProjectOperation accepted an invalid project ID")
	}
	if _, err := api.ProjectOperation(context.Background(), projectID.String(), "invalid"); err == nil {
		t.Fatal("ProjectOperation accepted an invalid operation ID")
	}
	if _, err := api.ProjectOperation(context.Background(), projectID.String(), operationID.String()); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("ProjectOperation() error = %v", err)
	}
}

func TestManagementClientBypassesProxy(t *testing.T) {
	address := nonLoopbackIPv4(t)
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxy must not be used", http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")

	listener, err := net.Listen("tcp4", net.JoinHostPort(address, "0"))
	if err != nil {
		t.Skipf("bind non-loopback listener: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.ListProjects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits := proxyHits.Load(); hits != 0 {
		t.Fatalf("management request used HTTP proxy %d times", hits)
	}
}

func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, networkInterface := range interfaces {
		addresses, err := networkInterface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	t.Skip("no non-loopback IPv4 address")
	return ""
}
