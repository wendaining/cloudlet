package sitehttp

import (
	"net/http"
	"testing"
)

func TestCombineETagHeaders(t *testing.T) {
	request := &http.Request{Header: http.Header{
		"If-Match":      {`"first,tag"`, `"second"`},
		"If-None-Match": {`"old"`, `W/"current"`},
	}}
	combined := combineETagHeaders(request)
	if got := combined.Header.Get("If-Match"); got != `"first,tag", "second"` {
		t.Fatalf("If-Match = %q", got)
	}
	if got := combined.Header.Get("If-None-Match"); got != `"old", W/"current"` {
		t.Fatalf("If-None-Match = %q", got)
	}
	for _, key := range []string{"If-Match", "If-None-Match"} {
		if len(request.Header.Values(key)) != 2 {
			t.Fatalf("%s changed on the original request: %v", key, request.Header)
		}
	}
}
