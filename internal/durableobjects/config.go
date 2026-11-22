package durableobjects

import (
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

// StoragePath is the writable volume mount inside the workerd container.
// The caller must grant access to only one running execution at a time.
const StoragePath = "/durable-objects"

var identifier = regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*$`)

// Namespace binds a named JavaScript class export in the Worker to a stable ID.
// Bindings lists the environment property names that refer to this namespace.
type Namespace struct {
	ID        uuid.UUID
	ClassName string
	Bindings  []string
}

// Validate checks the namespace declarations for one SQLite-backed Worker.
func Validate(namespaces []Namespace) error {
	if len(namespaces) == 0 {
		return fmt.Errorf("at least one Durable Object namespace is required")
	}
	ids := make(map[uuid.UUID]bool, len(namespaces))
	classes := make(map[string]bool, len(namespaces))
	bindings := make(map[string]bool)
	for _, namespace := range namespaces {
		if namespace.ID == uuid.Nil {
			return fmt.Errorf("Durable Object class %q requires a nonzero namespace ID", namespace.ClassName)
		}
		if ids[namespace.ID] {
			return fmt.Errorf("duplicate Durable Object namespace ID %s", namespace.ID)
		}
		if !identifier.MatchString(namespace.ClassName) || namespace.ClassName == "default" {
			return fmt.Errorf("Durable Object namespace %s class %q must be a named ASCII JavaScript export", namespace.ID, namespace.ClassName)
		}
		if classes[namespace.ClassName] {
			return fmt.Errorf("duplicate Durable Object class %q", namespace.ClassName)
		}
		if len(namespace.Bindings) == 0 {
			return fmt.Errorf("Durable Object namespace %s requires at least one binding", namespace.ID)
		}
		for _, name := range namespace.Bindings {
			if !identifier.MatchString(name) {
				return fmt.Errorf("Durable Object namespace %s binding %q must be an ASCII JavaScript identifier", namespace.ID, name)
			}
			if bindings[name] {
				return fmt.Errorf("duplicate Durable Object binding %q", name)
			}
			bindings[name] = true
		}
		ids[namespace.ID] = true
		classes[namespace.ClassName] = true
	}
	return nil
}
