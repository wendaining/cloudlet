package workerpublish

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDurableObjectNamespaceIdentity(t *testing.T) {
	project := uuid.New()
	request := Request{DurableObjects: []DurableObjectBinding{{ClassName: "Store", Bindings: []string{"ONE", "TWO"}}}}
	namespaces, err := request.namespaces(project, "worker")
	if err != nil || len(namespaces) != 1 || namespaces[0].ID == uuid.Nil || len(namespaces[0].Bindings) != 2 {
		t.Fatalf("namespace declaration: %+v, %v", namespaces, err)
	}

	for _, test := range []struct {
		project uuid.UUID
		worker  string
		class   string
		same    bool
	}{
		{project: project, worker: "worker", class: "Store", same: true},
		{project: uuid.New(), worker: "worker", class: "Store"},
		{project: project, worker: "other", class: "Store"},
		{project: project, worker: "worker", class: "Other"},
	} {
		request.DurableObjects[0] = DurableObjectBinding{ClassName: test.class, Bindings: []string{"RENAMED"}}
		result, err := request.namespaces(test.project, test.worker)
		if err != nil || len(result) != 1 || (result[0].ID == namespaces[0].ID) != test.same {
			t.Fatalf("namespace identity for %s/%s/%s: %+v, %v", test.project, test.worker, test.class, result, err)
		}
	}
}

func TestDurableObjectDeclarationLimits(t *testing.T) {
	for _, declarations := range [][]DurableObjectBinding{
		make([]DurableObjectBinding, 17),
		{{ClassName: strings.Repeat("a", 129), Bindings: []string{"STORE"}}},
		{{ClassName: "Store", Bindings: make([]string, 17)}},
		{{ClassName: "Store", Bindings: []string{strings.Repeat("a", 129)}}},
	} {
		_, err := (Request{DurableObjects: declarations}).namespaces(uuid.New(), "worker")
		if !errors.Is(err, fs.ErrInvalid) {
			t.Fatalf("oversized declarations returned %v", err)
		}
	}
}
