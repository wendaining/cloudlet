package sitehttp

import (
	"net/http"
	"testing"
	"time"
)

func TestRevalidation(t *testing.T) {
	const etag = `"current"`
	lastModified := time.Date(2025, time.March, 10, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		tags  []string
		since string
		want  bool
	}{
		{name: "no validator"},
		{name: "matches weak tag in list", tags: []string{`"old,tag", W/"current"`}, want: true},
		{name: "multiple field lines", tags: []string{`"old"`, `"current"`}, want: true},
		{name: "wildcard", tags: []string{"*"}, want: true},
		{name: "invalid tag", tags: []string{`W/"current"garbage`}},
		{name: "unclosed tag", tags: []string{`"current`}},
		{name: "matches by modification date", since: lastModified.Format(http.TimeFormat), want: true},
		{name: "older modification date", since: lastModified.Add(-time.Second).Format(http.TimeFormat)},
		{name: "invalid modification date", since: "not a date"},
		{name: "entity tag takes precedence", tags: []string{`"older"`}, since: lastModified.Format(http.TimeFormat)},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"If-None-Match": test.tags, "If-Modified-Since": {test.since}}
			if got := notModified(headers, etag, lastModified); got != test.want {
				t.Fatalf("notModified(%v) = %v, want %v", headers, got, test.want)
			}
		})
	}
	if notModified(http.Header{"If-Modified-Since": {lastModified.Format(http.TimeFormat)}}, etag, time.Time{}) {
		t.Fatal("date must not match a file without a known modification time")
	}
}
