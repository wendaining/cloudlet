package browser

import (
	"strings"
	"testing"
)

func TestValidateClickInputs(t *testing.T) {
	if !validClick("button#counter", 30000) || !validClick(strings.Repeat("界", 1024), 1) {
		t.Fatal("valid click input was rejected")
	}
	for _, selector := range []string{"", "  ", "button\x00", strings.Repeat("界", 1025), strings.Repeat("😀", 513)} {
		if validClick(selector, 200) {
			t.Errorf("invalid selector accepted: %q", selector)
		}
	}
	if validClick("button", 0) || validClick("button", 30001) {
		t.Fatal("out-of-range timeout accepted")
	}
}
