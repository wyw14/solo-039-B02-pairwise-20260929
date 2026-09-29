package feed

import (
	"context"
	"errors"
	"fmt"
	"github.com/wyw14/solo-039/internal/model"
)

// Switch outcomes reported through SwitchError.Outcome.
const (
	// OutcomeReverted means the previous feed source was re-opened and the plant
	// keeps feeding from it; the attempted switch left no pending change behind.
	OutcomeReverted = "reverted"
	// OutcomeIsolated means neither the new nor the previous source is active. The
	// plant is held in a safe, feed-less stop and must not resume automatically.
	OutcomeIsolated = "isolated"
)

// Switch stages at which a source change can fail.
const (
	stageActivateNew = "activate-new-source"
	stageRestoreOld  = "restore-old-source"
)

// SwitchError describes the aftermath of a failed feed switch so callers can
// reconcile batch state with feed state and tell an operator how to recover.
type SwitchError struct {
	// Stage is the step that failed.
	Stage string
	// Source is the new source that could not be brought online.
	Source string
	// Outcome is either OutcomeReverted or OutcomeIsolated.
	Outcome string
	// Reverted is true when the previous source is feeding again.
	Reverted bool
	err      error
}

func (e *SwitchError) Error() string {
	switch e.Outcome {
	case OutcomeReverted:
		return fmt.Sprintf("feed switch to source %q failed at %s: %v; previous source re-opened, retry the grade change", e.Source, e.Stage, e.err)
	case OutcomeIsolated:
		return fmt.Sprintf("feed switch to source %q failed at %s: %v; feed isolated in a safe stop, restore an available source before restart", e.Source, e.Stage, e.err)
	default:
		return fmt.Sprintf("feed switch to source %q failed at %s: %v", e.Source, e.Stage, e.err)
	}
}

func (e *SwitchError) Unwrap() error { return e.err }

func switchError(stage, source, outcome string, err error) *SwitchError {
	return &SwitchError{Stage: stage, Source: source, Outcome: outcome, Reverted: outcome == OutcomeReverted, err: err}
}

type Transition struct {
	Stage     string
	OldSource string
	OldOpen   bool
	OldFlow   float64
	NewSource string
	NewOpen   bool
}

type Coordinator struct {
	Current  *Route
	Previous *Route
	Observe  func(Transition)
	// Available reports whether a named feed source can currently be brought
	// online. When nil every non-empty source is considered available. It is
	// consulted at the moment a route is activated, so a source that becomes
	// unavailable mid-switch (including the previous source during rollback) is
	// detected rather than assumed.
	Available func(source string) bool
}

func NewCoordinator() *Coordinator { return &Coordinator{Current: &Route{}, Previous: &Route{}} }

// activate brings a route online only when its source is non-empty and
// currently available.
func (c *Coordinator) activate(route *Route) error {
	if route.Source == "" {
		return errors.New("source required")
	}
	if c.Available != nil && !c.Available(route.Source) {
		return fmt.Errorf("feed source %q is unavailable", route.Source)
	}
	return route.Activate()
}

// Switch changes the active feed source. It is all-or-nothing with respect to
// the observable feed state:
//
//   - On success the old route is isolated and the new route is active.
//   - If the new source cannot be activated, the old route is re-opened
//     (OutcomeReverted), so the plant keeps feeding from the prior source.
//   - If the old route cannot be re-opened, both routes stay isolated
//     (OutcomeIsolated): a safe stop rather than an ambiguous half-switch.
func (c *Coordinator) Switch(ctx context.Context, source string, campaign model.Campaign) error {
	old := c.Current
	priorPrevious := c.Previous
	hadOld := old != nil && old.Source != ""

	if hadOld {
		// Close mutates only after the ctx check, so a failure here leaves the old
		// route open and the coordinator untouched: no rollback is required and the
		// caller can abort before publishing any batch change.
		if err := old.Close(ctx); err != nil {
			return err
		}
		c.emit("old-isolated", old, nil)
	}

	next := &Route{}
	next.Prepare(source)
	if err := c.activate(next); err != nil {
		return c.rollforwardFailed(source, old, priorPrevious, hadOld, err)
	}

	c.Previous = old
	c.Current = next
	c.emit("new-active", old, next)
	return nil
}

// rollforwardFailed is invoked after the old source has been isolated but the
// new source cannot be activated. It either restores the old feed or holds the
// plant isolated.
func (c *Coordinator) rollforwardFailed(source string, old, priorPrevious *Route, hadOld bool, cause error) error {
	if !hadOld {
		// No prior feed to fall back to: keep the prepared (but inactive) route as
		// the isolated current route so the safe-stop state is explicit.
		c.Current = &Route{}
		c.Current.Prepare(source)
		c.Previous = priorPrevious
		return switchError(stageActivateNew, source, OutcomeIsolated, cause)
	}

	if reopenErr := c.activate(old); reopenErr != nil {
		// The old source cannot be brought back either. Leave it isolated and keep
		// Current pointing at it so observers see a feed-less plant.
		c.Current = old
		c.Previous = priorPrevious
		return switchError(stageRestoreOld, source, OutcomeIsolated, errors.Join(cause, reopenErr))
	}

	// Old feed restored: the switch is fully unwound with no pending route.
	c.Current = old
	c.Previous = priorPrevious
	c.emit("old-reopened", nil, old)
	return switchError(stageActivateNew, source, OutcomeReverted, cause)
}

func (c *Coordinator) emit(stage string, old, next *Route) {
	if c.Observe == nil {
		return
	}
	value := Transition{Stage: stage}
	if old != nil {
		value.OldSource = old.Source
		value.OldOpen = old.Open
		value.OldFlow = old.Flow
	}
	if next != nil {
		value.NewSource = next.Source
		value.NewOpen = next.Open
	}
	c.Observe(value)
}

func (c *Coordinator) SafeSwitch(ctx context.Context, source string, campaign model.Campaign) error {
	if c.Current.Source != "" && !c.Current.Isolated() {
		return errors.New("old source is not isolated")
	}
	return c.Switch(ctx, source, campaign)
}
