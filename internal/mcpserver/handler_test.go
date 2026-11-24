package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/wendaining/cloudlet/internal/apigen"
)

func TestMappingPreservesEmptyCollections(t *testing.T) {
	if projects := mapProjects(nil); projects == nil || len(projects) != 0 {
		t.Fatalf("mapProjects(nil) = %#v, want non-nil empty slice", projects)
	}
	if operations := mapOperations(nil); operations == nil || len(operations) != 0 {
		t.Fatalf("mapOperations(nil) = %#v, want non-nil empty slice", operations)
	}
}

func TestMappingFormatsProjectAndOperation(t *testing.T) {
	projectID := uuid.New()
	operationID := uuid.New()
	createdAt := time.Date(2026, 11, 23, 12, 34, 56, 123456789, time.UTC)
	updatedAt := createdAt.Add(time.Second)

	project := mapProject(apigen.Project{Id: projectID, Name: "alpha"})
	if project.ID != projectID.String() || project.Name != "alpha" {
		t.Fatalf("mapProject() = %#v", project)
	}

	operation := mapOperation(apigen.Operation{
		Id:        operationID,
		Action:    "deploy",
		Phase:     "preparing",
		Outcome:   "in_progress",
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	})
	if operation.ID != operationID.String() || operation.Action != "deploy" ||
		operation.Phase != "preparing" || operation.Outcome != "in_progress" ||
		operation.CreatedAt != createdAt.Format(time.RFC3339Nano) ||
		operation.UpdatedAt != updatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("mapOperation() = %#v", operation)
	}
}

func TestRequestClientRequiresRequestMetadata(t *testing.T) {
	_, err := requestClient("http://127.0.0.1:8080", &mcp.CallToolRequest{})
	if err == nil || !strings.Contains(err.Error(), "missing MCP HTTP request metadata") {
		t.Fatalf("requestClient() error = %v", err)
	}
}

func TestNewHandlerRejectsInvalidManagementEndpoint(t *testing.T) {
	if _, err := NewHandler("file:///tmp/cloudlet.sock"); err == nil {
		t.Fatal("NewHandler accepted an invalid management endpoint")
	}
}
