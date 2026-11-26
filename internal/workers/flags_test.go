package workers

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestPrepareCompatibilityFlags(t *testing.T) {
	flags := []string{"nodejs_compat", "allow_custom_ports"}
	prepared, err := PrepareModuleWithFlags([]byte("export default {};"), "2026-10-12", flags)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"allow_custom_ports", "nodejs_compat"}; !slices.Equal(prepared.CompatibilityFlags, want) || flags[0] != "nodejs_compat" {
		t.Fatalf("canonical flags = %v, input = %v", prepared.CompatibilityFlags, flags)
	}
	field := []byte(`compatibilityFlags = ["allow_custom_ports", "nodejs_compat"]`)
	if !bytes.Contains(prepared.Config, field) || !bytes.Contains(preflightConfig(prepared.CompatibilityDate, prepared.CompatibilityFlags), field) {
		t.Fatalf("serve and preflight configs must both declare %s", field)
	}
	original, err := PrepareModule([]byte("export default {};"), "2026-10-12")
	if err != nil || bytes.Contains(original.Config, []byte("compatibilityFlags")) {
		t.Fatalf("omitted flags changed the default config: %v", err)
	}
}

func TestInvalidCompatibilityFlags(t *testing.T) {
	for _, test := range []struct {
		input []string
		want  string
	}{
		{input: make([]string, MaxCompatibilityFlags+1), want: "at most"},
		{input: []string{"nodejs_compat", "nodejs_compat"}, want: "duplicate"},
		{input: []string{""}, want: "invalid"},
		{input: []string{`nodejs_compat", test`}, want: "invalid"},
		{input: []string{strings.Repeat("a", 65)}, want: "invalid"},
	} {
		_, err := PrepareModuleWithFlags([]byte("export default {};"), "2026-10-12", test.input)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("flags %q: error %v, want %q", test.input, err, test.want)
		}
	}
}
