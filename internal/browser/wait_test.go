package browser

import (
	"strings"
	"testing"
)

func TestPageWaitValidation(t *testing.T) {
	for _, entry := range []struct {
		name  string
		input PageWait
		valid bool
	}{
		{"ready", PageWait{Condition: "ready", Timeout: 1}, true},
		{"text", PageWait{"text", new("加载成功"), 30000}, true},
		{"unknown", PageWait{Condition: "idle", Timeout: 1000}, false},
		{"unexpected text", PageWait{"ready", new("marker"), 1000}, false},
		{"missing text", PageWait{Condition: "text", Timeout: 1000}, false},
		{"blank", PageWait{"text", new(" "), 1000}, false},
		{"null byte", PageWait{"text", new("a\x00b"), 1000}, false},
		{"too many units", PageWait{"text", new(strings.Repeat("🎉", 513)), 1000}, false},
		{"zero timeout", PageWait{Condition: "ready"}, false},
		{"timeout above maximum", PageWait{Condition: "ready", Timeout: 30001}, false},
	} {
		t.Run(entry.name, func(t *testing.T) {
			if got := entry.input.valid(); got != entry.valid {
				t.Fatalf("valid() = %t, want %t", got, entry.valid)
			}
		})
	}
}
