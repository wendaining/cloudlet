package controlplane

import (
	"strings"
	"testing"
)

func TestNormalizeProjectName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		valid bool
	}{
		{name: "trim surrounding whitespace", input: "  project name  ", want: "project name", valid: true},
		{name: "unicode character limit", input: strings.Repeat("项", 100), want: strings.Repeat("项", 100), valid: true},
		{name: "empty", input: "\t ", valid: false},
		{name: "too many unicode characters", input: strings.Repeat("项", 101), want: strings.Repeat("项", 101), valid: false},
		{name: "null character", input: "project\x00name", want: "project\x00name", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := normalizeProjectName(test.input)
			if got != test.want || valid != test.valid {
				t.Fatalf("normalizeProjectName(%q) = (%q, %t), want (%q, %t)", test.input, got, valid, test.want, test.valid)
			}
		})
	}
}
