package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
