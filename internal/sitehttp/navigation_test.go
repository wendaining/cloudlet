package sitehttp

import (
	"net/http"
	"testing"
)

func TestIsHTMLNavigation(t *testing.T) {
	for _, test := range []struct {
		name, mode, destination string
		accept                  []string
		want                    bool
	}{
		{"explicit HTML", "", "", []string{"text/html"}, true},
		{"browser navigation", "navigate", "iframe", []string{"text/html,application/xhtml+xml,*/*;q=0.8"}, true},
		{"weighted HTML", "navigate", "document", []string{"application/json", "text/html;q=0.5"}, true},
		{"default wildcard", "", "", []string{"*/*"}, false},
		{"rejected HTML", "navigate", "document", []string{"text/html;q=0"}, false},
		{"invalid quality", "", "", []string{"text/html;q=bogus"}, false},
		{"fetch with HTML accept", "cors", "empty", []string{"text/html"}, false},
		{"script destination", "", "script", []string{"text/html"}, false},
		{"no accept", "navigate", "document", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"Accept": test.accept, "Sec-Fetch-Mode": {test.mode}, "Sec-Fetch-Dest": {test.destination}}
			if got := isHTMLNavigation(headers); got != test.want {
				t.Fatalf("isHTMLNavigation(%v) = %v, want %v", headers, got, test.want)
			}
		})
	}
}
