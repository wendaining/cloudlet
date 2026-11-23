package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProjectsCommandTableAndJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "cloudlet_dev_session=session" {
			t.Errorf("Cookie = %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/projects" {
			_, _ = w.Write([]byte("[{\"id\":\"7d67b01a-0c2f-4de3-a032-a9ac3c11ef38\",\"name\":\"alpha\"}]"))
			return
		}
		_, _ = w.Write([]byte("{\"id\":\"7d67b01a-0c2f-4de3-a032-a9ac3c11ef38\",\"name\":\"alpha\"}"))
	}))
	defer server.Close()
	command := newProjectsCommand()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetArgs([]string{"--endpoint", server.URL, "--cookie", "cloudlet_dev_session=session", "list"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ID") || !strings.Contains(stdout.String(), "NAME") || !strings.Contains(stdout.String(), "alpha") {
		t.Fatalf("table output = %q", stdout.String())
	}

	command = newProjectsCommand()
	stdout.Reset()
	command.SetOut(&stdout)
	command.SetArgs([]string{"get", "7d67b01a-0c2f-4de3-a032-a9ac3c11ef38", "--endpoint", server.URL, "--cookie", "cloudlet_dev_session=session", "--output", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"name":"alpha"`) {
		t.Fatalf("JSON output = %q", stdout.String())
	}
}
