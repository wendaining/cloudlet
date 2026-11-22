package operations

import (
	"fmt"
	"slices"
)

type Phase string

const (
	Received   Phase = "received"
	Saving     Phase = "saving"
	Preflight  Phase = "preflight"
	Preparing  Phase = "preparing"
	Activating Phase = "activating"
	Finalizing Phase = "finalizing"
)

var phases = [...]Phase{Received, Saving, Preflight, Preparing, Activating, Finalizing}

type Outcome string

const (
	InProgress Outcome = "in_progress"
	Succeeded  Outcome = "succeeded"
	Failed     Outcome = "failed"
)

type Progress struct {
	Phase   Phase
	Outcome Outcome
}

func (next Progress) validateFrom(expected Phase) error {
	from := slices.Index(phases[:], expected)
	to := slices.Index(phases[:], next.Phase)
	if from >= 0 && to >= 0 {
		switch next.Outcome {
		case InProgress:
			if to > from {
				return nil
			}
		case Succeeded, Failed:
			if to == from {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %q 转为 %q（%q）", ErrInvalidTransition, expected, next.Phase, next.Outcome)
}
