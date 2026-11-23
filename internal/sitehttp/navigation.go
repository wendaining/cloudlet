package sitehttp

import (
	"mime"
	"net/http"
	"strconv"
	"strings"
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
		for item := range strings.SplitSeq(header, ",") {
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
