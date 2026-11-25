package browser

import (
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestValidNavigationURL(t *testing.T) {
	for _, value := range []string{"http://example.test/page", "https://example.test", "http://[::1]:8080/", "https://example.test/?next=%2Fx"} {
		if !validURL(value) {
			t.Errorf("validURL(%q) = false", value)
		}
	}
	for _, value := range []string{"", "file:///tmp/index.html", "//example.test/", "http:/path", "http://user:password@example.test", "https://example.test/" + strings.Repeat("x", 4096)} {
		if validURL(value) {
			t.Errorf("validURL(%q) = true", value)
		}
	}
}

func TestRuntimeCredentialsAreSessionScoped(t *testing.T) {
	service := &Service{key: []byte("integration-example-platform-key")}
	first, second := uuid.New(), uuid.New()
	token := service.token(first)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,}$`).MatchString(token) || service.token(first) != token {
		t.Fatal("runtime credentials must be stable for the same session and accepted by the browser image")
	}
	if service.token(second) == token {
		t.Fatal("distinct sessions share a runtime credential")
	}
}
