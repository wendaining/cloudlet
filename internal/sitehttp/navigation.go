package sitehttp

import (
	"mime"
	"net/http"
	"strconv"
)

func isHTMLNavigation(headers http.Header) bool {
	if mode := headers.Get("Sec-Fetch-Mode"); mode != "" && mode != "navigate" {
		return false
	}
	switch headers.Get("Sec-Fetch-Dest") {
	case "", "document", "frame", "iframe":
	default:
		return false
	}
	for _, header := range headers.Values("Accept") {
		for header != "" {
			var item string
			item, header = nextMediaRange(header)
			mediaType, parameters, err := mime.ParseMediaType(item)
			if err != nil || mediaType != "text/html" {
				continue
			}
			weight, explicit := parameters["q"]
			if !explicit {
				return true
			}
			quality, err := strconv.ParseFloat(weight, 64)
			if err == nil && quality > 0 && quality <= 1 {
				return true
			}
		}
	}
	return false
}

// Commas inside quoted media parameters do not delimit Accept items.
func nextMediaRange(header string) (item, rest string) {
	var quoted, escaped bool
	for pos, char := range header {
		switch {
		case escaped:
			escaped = false
		case quoted && char == '\\':
			escaped = true
		case char == '"':
			quoted = !quoted
		case !quoted && char == ',':
			return header[:pos], header[pos+1:]
		}
	}
	return header, ""
}
