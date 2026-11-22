package operations

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestProgressTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    Phase
		next    Progress
		allowed bool
	}{
		{"advance one phase", Received, Progress{Saving, InProgress}, true},
		{"skip unused phases", Received, Progress{Preparing, InProgress}, true},
		{"finish at current phase", Finalizing, Progress{Finalizing, Succeeded}, true},
		{"fail at current phase", Preflight, Progress{Preflight, Failed}, true},
		{"complete a short action", Received, Progress{Received, Succeeded}, true},
		{"stay in place", Saving, Progress{Saving, InProgress}, false},
		{"move backward", Preparing, Progress{Preflight, InProgress}, false},
		{"succeed while changing phase", Activating, Progress{Finalizing, Succeeded}, false},
		{"fail while changing phase", Preparing, Progress{Saving, Failed}, false},
		{"unknown current phase", "unknown", Progress{Saving, InProgress}, false},
		{"unknown next phase", Saving, Progress{"unknown", InProgress}, false},
		{"unknown outcome", Saving, Progress{Saving, "unknown"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.next.validateFrom(test.from)
			if test.allowed && err != nil || !test.allowed && !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("validateFrom returned %v, allowed = %t", err, test.allowed)
			}
		})
	}
}

func TestRegistrationValidation(t *testing.T) {
	projectID := uuid.New()
	action := strings.Repeat("a", 128)
	key := strings.Repeat("键", 85) + "a"
	tests := []struct {
		name    string
		request Registration
		valid   bool
	}{
		{"maximum lengths", Registration{projectID, action, key, "fingerprint"}, true},
		{"missing project", Registration{uuid.Nil, action, key, "fingerprint"}, false},
		{"action too long", Registration{projectID, action + "a", key, "fingerprint"}, false},
		{"key too long", Registration{projectID, action, key + "a", "fingerprint"}, false},
		{"blank action", Registration{projectID, " ", key, "fingerprint"}, false},
		{"blank key", Registration{projectID, action, " \t", "fingerprint"}, false},
		{"blank fingerprint", Registration{projectID, action, key, ""}, false},
		{"NUL in key", Registration{projectID, action, "\x00", "fingerprint"}, false},
		{"invalid UTF-8", Registration{projectID, action, key, "\xff"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.validate(); (err == nil) != test.valid {
				t.Fatalf("validate returned %v, valid = %t", err, test.valid)
			}
		})
	}
}
