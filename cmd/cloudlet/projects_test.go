package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestProjectOutputFormats(t *testing.T) {
	project := apigen.Project{Name: "alpha"}
	var stdout bytes.Buffer
	if err := writeProjects(&stdout, []apigen.Project{project}, "table"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ID") || !strings.Contains(stdout.String(), "NAME") || !strings.Contains(stdout.String(), "alpha") {
		t.Fatalf("table output = %q", stdout.String())
	}

	stdout.Reset()
	if err := writeProject(&stdout, project, "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"name":"alpha"`) {
		t.Fatalf("JSON output = %q", stdout.String())
	}
	stdout.Reset()
	if err := writeProjects(&stdout, nil, "table"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "ID") || strings.Contains(stdout.String(), "alpha") {
		t.Fatalf("empty table output = %q", stdout.String())
	}
}
