package sitehttp

import (
	"net/http"
	"strings"
	"time"
)

func cachePolicy(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return "no-cache"
	}
	return "public, max-age=300"
}

// notModified evaluates the revalidation headers for GET and HEAD.
func notModified(headers http.Header, etag string, lastModified time.Time) bool {
	if matches := headers.Values("If-None-Match"); len(matches) > 0 {
		for _, value := range matches {
			if matchesETag(value, etag) {
				return true
			}
		}
		return false
	}
	if lastModified.IsZero() {
		return false
	}
	since, err := http.ParseTime(headers.Get("If-Modified-Since"))
	return err == nil && !lastModified.After(since)
}

func matchesETag(value, want string) bool {
	value = strings.TrimSpace(value)
	if value == "*" {
		return true
	}
	for value != "" {
		value = strings.TrimLeft(value, ", \t")
		value = strings.TrimPrefix(value, "W/")
		quoted, ok := strings.CutPrefix(value, `"`)
		if !ok {
			return false
		}
		tag, rest, ok := strings.Cut(quoted, `"`)
		value = strings.TrimSpace(rest)
		if !ok || (value != "" && !strings.HasPrefix(value, ",")) {
			return false
		}
		if `"`+tag+`"` == want {
			return true
		}
	}
	return false
}
