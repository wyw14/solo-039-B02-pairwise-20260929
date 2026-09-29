package service

import (
	"context"
	"errors"
	"testing"

	"github.com/wyw14/solo-039/internal/journal"
	"github.com/wyw14/solo-039/internal/model"
)

func startedPlant(t *testing.T) *Plant {
	t.Helper()
	plant := NewPlant(t.TempDir())
	if _, err := plant.Start(context.Background(), "grade-a", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	return plant
}

func transitionFailure(t *testing.T, err error) *TransitionFailure {
	t.Helper()
	var failure *TransitionFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected *TransitionFailure, got %T: %v", err, err)
	}
	return failure
}

func journalConsistent(t *testing.T, plant *Plant) {
	t.Helper()
	if err := journal.ValidateRecovery(plant.Snapshot(), plant.Journal.Events()); err != nil {
		t.Fatalf("journal/snapshot inconsistent after failure: %v", err)
	}
}

// The headline defect: a named-but-unavailable new source must not leave the
// batch revision advanced and the old feed closed. The previous batch and feed
// are restored, with an explicit retry instruction.
func TestTransitionRollsBackWhenNewSourceUnavailable(t *testing.T) {
	plant := startedPlant(t)
	before := plant.Campaign.Snapshot()
	plant.Feed.Available = func(source string) bool { return source == "primary" }

	got, err := plant.Transition(context.Background(), "grade-b", "secondary")
	failure := transitionFailure(t, err)

	if !failure.RolledBack || failure.SafeStop {
		t.Fatalf("expected rollback, got rolled_back=%v safe_stop=%v", failure.RolledBack, failure.SafeStop)
	}
	if failure.RetryStep != RetryReissueTransition {
		t.Fatalf("retry step = %q, want %q", failure.RetryStep, RetryReissueTransition)
	}
	if failure.LandedPhase != model.Stabilizing {
		t.Fatalf("landed phase = %q, want stabilizing", failure.LandedPhase)
	}

	after := plant.Campaign.Snapshot()
	if after.Grade != "grade-a" {
		t.Fatalf("grade = %q, want restored grade-a", after.Grade)
	}
	if after.Revision != before.Revision {
		t.Fatalf("batch revision changed: before=%s after=%s", before.Revision, after.Revision)
	}
	if after.Epoch <= before.Epoch {
		t.Fatalf("epoch must advance monotonically: before=%d after=%d", before.Epoch, after.Epoch)
	}
	if got.Grade != "grade-a" || got.Revision != before.Revision {
		t.Fatalf("returned campaign does not match restored batch: %+v", got)
	}

	if plant.Feed.Current.Source != "primary" || !plant.Feed.Current.Open {
		t.Fatalf("feed not restored to primary: %+v", plant.Feed.Current)
	}
	if plant.Feed.Current.Isolated() {
		t.Fatal("old feed must be active after rollback")
	}
	if plant.Column.Snapshot().FeedRevision != plant.Feed.Current.Revision {
		t.Fatal("column feed revision not rebound to restored feed")
	}
	if plant.Status()["started"] != true {
		t.Fatal("plant must remain running after a rollback")
	}
	journalConsistent(t, plant)
}

// If neither the new nor the old source can be active, the plant settles in an
// unambiguous feed-less safe stop with heat removed, and recovery starts from
// restoring feed.
func TestTransitionSafeStopsWhenFeedCannotBeRestored(t *testing.T) {
	plant := startedPlant(t)
	before := plant.Campaign.Snapshot()
	plant.Feed.Available = func(source string) bool { return false }

	_, err := plant.Transition(context.Background(), "grade-b", "secondary")
	failure := transitionFailure(t, err)

	if failure.RolledBack || !failure.SafeStop {
		t.Fatalf("expected safe stop, got rolled_back=%v safe_stop=%v", failure.RolledBack, failure.SafeStop)
	}
	if failure.RetryStep != RetryResumeFeed {
		t.Fatalf("retry step = %q, want %q", failure.RetryStep, RetryResumeFeed)
	}

	after := plant.Campaign.Snapshot()
	if after.Phase != model.Stopped {
		t.Fatalf("phase = %q, want stopped", after.Phase)
	}
	if after.Revision == before.Revision {
		t.Fatal("safe stop must retain the attempted new revision, not silently revert")
	}
	if !plant.Feed.Current.Isolated() {
		t.Fatal("feed must be isolated in a safe stop")
	}
	if steam := plant.Reboiler.Snapshot().Steam; steam != 0 {
		t.Fatalf("residual steam %f in safe stop", steam)
	}
	if plant.Status()["started"] != false {
		t.Fatal("plant must report stopped after safe stop")
	}
	journalConsistent(t, plant)
}

// An invalid request is rejected before any state changes; nothing to roll back.
func TestTransitionRejectsInvalidSourceWithoutMutation(t *testing.T) {
	plant := startedPlant(t)
	before := plant.Campaign.Snapshot()
	feedBefore := *plant.Feed.Current

	_, err := plant.Transition(context.Background(), "grade-b", "  ")
	failure := transitionFailure(t, err)

	if failure.RolledBack || failure.SafeStop {
		t.Fatal("rejected request must not roll back or stop")
	}
	if failure.RetryStep != RetryChooseSource {
		t.Fatalf("retry step = %q, want %q", failure.RetryStep, RetryChooseSource)
	}
	after := plant.Campaign.Snapshot()
	if after != before {
		t.Fatalf("campaign mutated by rejected request: before=%+v after=%+v", before, after)
	}
	if plant.Feed.Current.Source != feedBefore.Source || !plant.Feed.Current.Open {
		t.Fatal("feed mutated by rejected request")
	}
}

// After a rollback the operator retries with an available source and succeeds.
func TestTransitionSucceedsOnRetryAfterRollback(t *testing.T) {
	plant := startedPlant(t)
	plant.Feed.Available = func(source string) bool { return source == "primary" }

	if _, err := plant.Transition(context.Background(), "grade-b", "secondary"); err == nil {
		t.Fatal("expected first transition to fail")
	}
	restored := plant.Campaign.Snapshot()

	// Source becomes available; retry the whole grade change.
	plant.Feed.Available = nil
	got, err := plant.Transition(context.Background(), "grade-b", "secondary")
	if err != nil {
		t.Fatalf("retry transition: %v", err)
	}
	if got.Grade != "grade-b" {
		t.Fatalf("grade = %q, want grade-b", got.Grade)
	}
	if got.Revision == restored.Revision {
		t.Fatal("retry must advance to a fresh revision")
	}
	if plant.Feed.Current.Source != "secondary" || !plant.Feed.Current.Open {
		t.Fatalf("feed not on secondary after retry: %+v", plant.Feed.Current)
	}
}
