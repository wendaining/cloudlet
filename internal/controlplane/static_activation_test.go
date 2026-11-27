package controlplane

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeActivationRevision(t *testing.T) {
	revision, err := decodeActivationRevision(strings.NewReader(`{"expected_revision":"2026-11-26T12:00:00.123456Z"}`))
	if want := time.Date(2026, 11, 26, 12, 0, 0, 123456000, time.UTC); err != nil || !revision.Equal(want) {
		t.Fatalf("revision = %s, err = %v, want %s", revision, err, want)
	}

	for _, body := range []string{
		"",
		"null",
		"{}",
		`{"expected_revision":null}`,
		`{"expected_revision":"0001-01-01T00:00:00Z"}`,
		`{"expected_revision":"yesterday"}`,
		`{"expected_revision":"2026-11-26T12:00:00Z","hostname":"other.localhost"}`,
		`{"expected_revision":"2026-11-26T12:00:00Z"} {}`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := decodeActivationRevision(strings.NewReader(body)); err == nil {
				t.Fatal("accepted invalid activation request")
			}
		})
	}
}
