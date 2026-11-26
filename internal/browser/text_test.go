package browser

import (
	"strings"
	"testing"
)

func TestValidateTextScope(t *testing.T) {
	if !validTextScope("#content > p:first-child") || !validTextScope(strings.Repeat("界", 1024)) {
		t.Fatal("valid CSS scope was rejected")
	}
	for _, selector := range []string{"", " ", "p\x00", strings.Repeat("😀", 513)} {
		if validTextScope(selector) {
			t.Errorf("invalid CSS scope accepted: %q", selector)
		}
	}
}
