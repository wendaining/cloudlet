package routes

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/google/uuid"
)

func TestNormalizeRoutesCanonicalizesAndValidates(t *testing.T) {
	projectID := uuid.NewString()
	records, err := normalizeRoutes(projectID, []Route{
		{Resource: "worker-a", Host: "APP.localhost.:8081", Target: "http://127.0.0.1:9000"},
		{Resource: "worker-b", Host: "app.localhost", PathPrefix: "/api", Target: "https://upstream.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Host != "app.localhost" || records[0].PathPrefix != "/" || records[0].ProjectID != projectID {
		t.Fatalf("first record = %+v", records[0])
	}
	if records[1].PathPrefix != "/api" {
		t.Fatalf("second record = %+v", records[1])
	}
}

func TestNormalizeRoutesRejectsInvalidInput(t *testing.T) {
	projectID := uuid.NewString()
	for _, tt := range []struct {
		name    string
		project string
		routes  []Route
	}{
		{"invalid project", "not-a-uuid", []Route{{Resource: "worker", Host: "app.localhost", Target: "http://127.0.0.1:9000"}}},
		{"empty resource", projectID, []Route{{Host: "app.localhost", Target: "http://127.0.0.1:9000"}}},
		{"invalid resource", projectID, []Route{{Resource: "worker/a", Host: "app.localhost", Target: "http://127.0.0.1:9000"}}},
		{"invalid host", projectID, []Route{{Resource: "worker", Host: "app.localhost/path", Target: "http://127.0.0.1:9000"}}},
		{"invalid target", projectID, []Route{{Resource: "worker", Host: "app.localhost", Target: "file:///etc/passwd"}}},
		{"invalid prefix", projectID, []Route{{Resource: "worker", Host: "app.localhost", PathPrefix: "api", Target: "http://127.0.0.1:9000"}}},
		{"duplicate route", projectID, []Route{
			{Resource: "worker-a", Host: "APP.localhost.", Target: "http://127.0.0.1:9000"},
			{Resource: "worker-b", Host: "app.localhost", Target: "http://127.0.0.1:9001"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := normalizeRoutes(tt.project, tt.routes); err == nil || !errors.Is(err, fs.ErrInvalid) {
				t.Fatalf("normalizeRoutes error = %v, want fs.ErrInvalid", err)
			}
		})
	}
}

func TestNewPublisherRequiresDependencies(t *testing.T) {
	if _, err := NewPublisher(nil, nil); err == nil || !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("NewPublisher error = %v, want fs.ErrInvalid", err)
	}
}

func TestSyncErrorUnwraps(t *testing.T) {
	sentinel := errors.New("apply failed")
	err := &SyncError{Version: 4, Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Fatalf("SyncError does not unwrap: %v", err)
	}
}
