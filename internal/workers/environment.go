package workers

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	MaxEnvironmentBytes      = 64 << 10
	maxEnvironmentValueBytes = 4 << 10
	maxEnvironmentBindings   = 64
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// EnvironmentBinding is a text binding and the immutable value embedded by workerd.
type EnvironmentBinding struct {
	Name  string
	Value string
}

func (b EnvironmentBinding) FilePath() string {
	return fmt.Sprintf("environment/%s-%x.txt", b.Name, sha256.Sum256([]byte(b.Value)))
}

func prepareEnvironment(values map[string]string) ([]EnvironmentBinding, error) {
	if len(values) > maxEnvironmentBindings {
		return nil, fmt.Errorf("at most %d Worker environment variables are allowed", maxEnvironmentBindings)
	}
	var bindings []EnvironmentBinding
	total := 0
	for _, name := range slices.Sorted(maps.Keys(values)) {
		value := values[name]
		if !environmentName.MatchString(name) {
			return nil, fmt.Errorf("invalid Worker environment variable name %q", name)
		}
		if len(value) > maxEnvironmentValueBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("Worker environment variable %q must contain valid UTF-8 without NUL, at most %d bytes", name, maxEnvironmentValueBytes)
		}
		total += len(value)
		if total > MaxEnvironmentBytes {
			return nil, fmt.Errorf("Worker environment variable values exceed %d bytes", MaxEnvironmentBytes)
		}
		bindings = append(bindings, EnvironmentBinding{Name: name, Value: value})
	}
	return bindings, nil
}

func appendEnvironmentBindingEntries(dst []byte, bindings []EnvironmentBinding) []byte {
	for _, binding := range bindings {
		dst = fmt.Appendf(dst, "    (name = %q, text = embed %q),\n", binding.Name, binding.FilePath())
	}
	return dst
}

func renderBindings(entries []byte) []byte {
	if len(entries) == 0 {
		return nil
	}
	result := append([]byte("  bindings = [\n"), entries...)
	return append(result, "  ],\n"...)
}
