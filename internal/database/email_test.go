package database

import (
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	longEmail := strings.Repeat("a", 168) + strings.Repeat("é", 40) + "@example.com"
	maxEmail := strings.Repeat("a", 242) + "@example.com"
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"normalizes case and whitespace", "  Dev@Example.COM  ", "dev@example.com", false},
		{"accepts long unicode email", longEmail, longEmail, false},
		{"accepts maximum characters", maxEmail, maxEmail, false},
		{"rejects empty", "", "", true},
		{"rejects invalid format", "not-an-email", "", true},
		{"rejects display name", "Dev <dev@example.com>", "", true},
		{"rejects too many characters", strings.Repeat("a", 243) + "@example.com", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeEmail(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("NormalizeEmail(%q) = %q, want error", test.input, got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeEmail(%q) = (%q, %v), want %q", test.input, got, err, test.want)
			}
		})
	}
}
