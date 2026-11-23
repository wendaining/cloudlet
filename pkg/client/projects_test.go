package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProjectsUseSessionAndHandleAccessErrors(t *testing.T) {
	const projectID = "7d67b01a-0c2f-4de3-a032-a9ac3c11ef38"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "cloudlet_dev_session=existing-session" {
			t.Errorf("Cookie header = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/projects":
			_, _ = w.Write([]byte("[{\"id\":\"" + projectID + "\",\"name\":\"alpha\"}]"))
		case "/api/v1/projects/" + projectID:
			_, _ = w.Write([]byte("{\"id\":\"" + projectID + "\",\"name\":\"alpha\"}"))
		case "/api/v1/projects/hidden":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("{\"message\":\"项目不存在或没有访问权限\"}"))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	api, err := New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("cloudlet_dev_session=existing-session"); err != nil {
		t.Fatal(err)
	}
	projects, err := api.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Id.String() != projectID || projects[0].Name != "alpha" {
		t.Fatalf("ListProjects() = %#v", projects)
	}
	project, err := api.GetProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	if project.Id.String() != projectID || project.Name != "alpha" {
		t.Fatalf("GetProject() = %#v", project)
	}
	if _, err := api.GetProject(context.Background(), "hidden"); err == nil {
		t.Fatal("GetProject(hidden) succeeded")
	}
}

func TestSetSessionCookieRejectsHeaderInjection(t *testing.T) {
	api, err := New("http://127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("valid\r\nX-Injected: yes"); err == nil {
		t.Fatal("SetSessionCookie accepted a newline")
	}
}

func TestProjectRequestDoesNotFollowRedirect(t *testing.T) {
	reachedTarget := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedTarget = true
		if r.Header.Get("Cookie") != "" {
			t.Errorf("redirect target received Cookie header %q", r.Header.Get("Cookie"))
		}
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/collect", http.StatusFound)
	}))
	defer source.Close()

	api, err := New(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.SetSessionCookie("cloudlet_dev_session=secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.ListProjects(context.Background()); err == nil {
		t.Fatal("ListProjects followed a redirect instead of reporting the 302")
	}
	if reachedTarget {
		t.Fatal("redirect target was contacted")
	}
}
