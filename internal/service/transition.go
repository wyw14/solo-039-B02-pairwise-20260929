package service

import (
	"errors"
	"fmt"

	"github.com/wyw14/solo-039/internal/feed"
	"github.com/wyw14/solo-039/internal/model"
)

// Retry steps an operator must resume from after a failed grade transition.
const (
	// RetryChooseSource: the requested feed source was rejected before anything
	// changed. Pick an available source and re-issue the grade transition.
	RetryChooseSource = "select-available-feed-source"
	// RetryResumeFeed: the plant is held in a safe stop with no feed. Re-establish
	// an available feed source, then restart the campaign.
	RetryResumeFeed = "restore-feed-then-restart-campaign"
	// RetryReissueTransition: the previous batch and feed were fully restored.
	// Re-issue the grade transition with an available source from the beginning.
	RetryReissueTransition = "retry-grade-transition"
)

// TransitionFailure is the single, coherent verdict returned when a grade
// transition does not complete. It reconciles the batch revision with the feed
// state and names the exact step the operator should retry from.
type TransitionFailure struct {
	// Grade and Source that were requested.
	Grade  string
	Source string
	// FailedStage is the feed/switch step that failed.
	FailedStage string
	// RolledBack is true when the prior batch revision and its feed were restored.
	RolledBack bool
	// SafeStop is true when the plant was placed in a feed-less safe stop.
	SafeStop bool
	// LandedPhase is the campaign phase the plant actually settled in.
	LandedPhase model.Phase
	// RetryStep is one of the Retry* constants: where the operator resumes.
	RetryStep string
	// RetryHint is a human-readable instruction.
	RetryHint string
	cause     error
}

func (f *TransitionFailure) Error() string {
	return fmt.Sprintf("grade transition to %q with source %q failed: %v. outcome=%s phase=%s retry=%s (%s)",
		f.Grade, f.Source, f.cause, f.outcome(), f.LandedPhase, f.RetryStep, f.RetryHint)
}

func (f *TransitionFailure) outcome() string {
	switch {
	case f.RolledBack:
		return "rolled-back-to-previous-batch"
	case f.SafeStop:
		return "safe-stop-feed-isolated"
	default:
		return "rejected-before-change"
	}
}

func (f *TransitionFailure) Unwrap() error { return f.cause }

// asSwitchError extracts the structured feed verdict, if present.
func asSwitchError(err error) (*feed.SwitchError, bool) {
	var target *feed.SwitchError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
