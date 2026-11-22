package durableobjects_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/wendaining/cloudlet/internal/durableobjects"
)

func TestValidateNamespaces(t *testing.T) {
	valid := durableobjects.Namespace{ID: uuid.New(), ClassName: "Counter", Bindings: []string{"COUNTER", "$counter"}}
	other := durableobjects.Namespace{ID: uuid.New(), ClassName: "_Settings2", Bindings: []string{"SETTINGS"}}
	tests := []struct {
		name       string
		namespaces []durableobjects.Namespace
		diagnostic string
	}{
		{"multiple namespaces and aliases", []durableobjects.Namespace{valid, other}, ""},
		{"no namespaces", nil, "at least one Durable Object namespace"},
		{"missing ID", []durableobjects.Namespace{{ClassName: valid.ClassName, Bindings: valid.Bindings}}, "nonzero namespace ID"},
		{"duplicate ID", []durableobjects.Namespace{valid, {ID: valid.ID, ClassName: other.ClassName, Bindings: other.Bindings}}, "duplicate Durable Object namespace ID"},
		{"missing class", []durableobjects.Namespace{{ID: valid.ID, Bindings: valid.Bindings}}, "must be a named ASCII JavaScript export"},
		{"invalid class", []durableobjects.Namespace{{ID: valid.ID, ClassName: `Bad"Class`, Bindings: valid.Bindings}}, "must be a named ASCII JavaScript export"},
		{"default class", []durableobjects.Namespace{{ID: valid.ID, ClassName: "default", Bindings: valid.Bindings}}, "must be a named ASCII JavaScript export"},
		{"duplicate class", []durableobjects.Namespace{valid, {ID: other.ID, ClassName: valid.ClassName, Bindings: other.Bindings}}, "duplicate Durable Object class"},
		{"no bindings", []durableobjects.Namespace{{ID: valid.ID, ClassName: valid.ClassName}}, "requires at least one binding"},
		{"invalid binding", []durableobjects.Namespace{{ID: valid.ID, ClassName: valid.ClassName, Bindings: []string{"1STORE"}}}, "must be an ASCII JavaScript identifier"},
		{"duplicate alias", []durableobjects.Namespace{{ID: valid.ID, ClassName: valid.ClassName, Bindings: []string{"A", "A"}}}, "duplicate Durable Object binding"},
		{"duplicate binding across namespaces", []durableobjects.Namespace{valid, {ID: other.ID, ClassName: other.ClassName, Bindings: valid.Bindings}}, "duplicate Durable Object binding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := durableobjects.Validate(tt.namespaces)
			if tt.diagnostic == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.diagnostic) {
				t.Fatalf("Validate returned %v, want %q", err, tt.diagnostic)
			}
		})
	}
}
