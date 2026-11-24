package staticdeploy

import (
	"strings"
	"testing"
)

func TestValidSiteName(t *testing.T) {
	for _, name := range []string{"a", "site-01", strings.Repeat("a", 63)} {
		if !validName(name) {
			t.Errorf("valid site name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "Upper", "-site", "site-", "site--two", "../site", "站点", strings.Repeat("a", 64)} {
		if validName(name) {
			t.Errorf("invalid site name accepted: %q", name)
		}
	}
}
