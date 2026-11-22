package workers

import (
	"strings"
	"testing"
	"time"
)

func TestCompatibilityDateUsesUTC(t *testing.T) {
	const date = "2026-10-19"
	for _, tt := range []struct {
		name, now string
		future    bool
	}{
		{"local date has started before UTC midnight", "2026-10-19T13:59:59+14:00", true},
		{"UTC date starts at midnight", "2026-10-19T14:00:00+14:00", false},
		{"UTC date has started before the local date", "2026-10-18T17:00:00-07:00", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			err = validateCompatibilityDate(date, now)
			if tt.future {
				if err == nil || !strings.Contains(err.Error(), `compatibility date "2026-10-19" is in the future (UTC)`) {
					t.Fatalf("validateCompatibilityDate(%q, %s) returned %v, want a future date error", date, tt.now, err)
				}
			} else if err != nil {
				t.Fatalf("validateCompatibilityDate(%q, %s): %v", date, tt.now, err)
			}
		})
	}
}
