package workers

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestEnvironmentSnapshot(t *testing.T) {
	values := map[string]string{"Z": "", "A": "quoted \"text\"\n你好"}
	prepared, err := PrepareModuleWithEnvironment([]byte("export default {};"), "2026-10-12", values)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Environment) != 2 || prepared.Environment[0].Name != "A" || prepared.Environment[1].Name != "Z" {
		t.Fatalf("prepared environment: %+v", prepared.Environment)
	}
	original := prepared.Environment[0]
	if !bytes.Contains(prepared.Config, []byte(original.FilePath())) ||
		!bytes.Contains(preflightConfig(prepared.CompatibilityDate, prepared.Environment), []byte(original.FilePath())) {
		t.Fatal("the same text artifact must be embedded at preflight and runtime")
	}
	values["A"] = "new value"
	next, err := PrepareModuleWithEnvironment([]byte("export default {};"), "2026-10-12", values)
	if err != nil || original.Value == next.Environment[0].Value || bytes.Equal(prepared.Config, next.Config) {
		t.Fatalf("changed value did not produce an independent snapshot: %v", err)
	}
}

func TestEnvironmentLimits(t *testing.T) {
	many := func(count, size int) map[string]string {
		values := make(map[string]string, count)
		for index := range count {
			values[fmt.Sprintf("VARIABLE_%d", index)] = strings.Repeat("a", size)
		}
		return values
	}
	boundary := many(MaxEnvironmentBytes/maxEnvironmentValueBytes, maxEnvironmentValueBytes)
	if _, err := prepareEnvironment(boundary); err != nil {
		t.Fatal(err)
	}
	for _, values := range []map[string]string{
		{"VAR/invalid": "x"},
		{"a": "\x00"},
		{"a": "\xff"},
		{"a": strings.Repeat("a", maxEnvironmentValueBytes+1)},
		many(maxEnvironmentBindings+1, 0),
		many(MaxEnvironmentBytes/maxEnvironmentValueBytes+1, maxEnvironmentValueBytes),
	} {
		if _, err := prepareEnvironment(values); err == nil {
			t.Fatalf("accepted invalid environment with %d values", len(values))
		}
	}
}
