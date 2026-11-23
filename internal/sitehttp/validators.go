package sitehttp

import (
	"net/http"
	"strings"
)

func cachePolicy(contentType string) string {
	mediaType, _, _ := strings.Cut(contentType, ";")
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return "no-cache"
	}
	return "public, max-age=300"
}

// Entity tag list fields can appear on multiple header lines. ServeContent
// reads the first value, so present the lines as a single list.
func combineETagHeaders(request *http.Request) *http.Request {
	for _, name := range []string{"If-Match", "If-None-Match"} {
		values := request.Header.Values(name)
		if len(values) > 1 {
			request = request.WithContext(request.Context())
			request.Header = request.Header.Clone()
			request.Header.Set(name, strings.Join(values, ", "))
		}
	}
	return request
}
