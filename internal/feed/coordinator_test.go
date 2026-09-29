package feed

import (
	"context"
	"errors"
	"testing"

	"github.com/wyw14/solo-039/internal/model"
)

func TestSwitchClosesPreviousRoute(t *testing.T) {
	coordinator := NewCoordinator()
	coordinator.Current.Prepare("A")
	if err := coordinator.Current.Activate(); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade")); err != nil {
		t.Fatal(err)
	}
	if !coordinator.Previous.Isolated() {
		t.Fatal("previous route remains active")
	}
	if !coordinator.Current.Open {
		t.Fatal("new route is closed")
	}
}

func activeRoute(t *testing.T, c *Coordinator, source string) {
	t.Helper()
	c.Current.Prepare(source)
	if err := c.Current.Activate(); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchRevertsWhenNewSourceUnavailable(t *testing.T) {
	coordinator := NewCoordinator()
	activeRoute(t, coordinator, "A")
	coordinator.Available = func(source string) bool { return source == "A" }

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	var failure *SwitchError
	if !errors.As(err, &failure) {
		t.Fatalf("expected SwitchError, got %v", err)
	}
	if failure.Outcome != OutcomeReverted || !failure.Reverted {
		t.Fatalf("expected reverted outcome, got %q", failure.Outcome)
	}
	if coordinator.Current.Source != "A" {
		t.Fatalf("current source = %q, want restored A", coordinator.Current.Source)
	}
	if !coordinator.Current.Open || coordinator.Current.Flow == 0 {
		t.Fatal("previous source A was not re-opened")
	}
	if coordinator.Current.Isolated() {
		t.Fatal("plant must keep feeding from A after rollback")
	}
}

func TestSwitchIsolatesWhenBothSourcesUnavailable(t *testing.T) {
	coordinator := NewCoordinator()
	activeRoute(t, coordinator, "A")
	coordinator.Available = func(source string) bool { return false }

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	var failure *SwitchError
	if !errors.As(err, &failure) {
		t.Fatalf("expected SwitchError, got %v", err)
	}
	if failure.Outcome != OutcomeIsolated || failure.Reverted {
		t.Fatalf("expected isolated outcome, got %q", failure.Outcome)
	}
	if coordinator.Current.Source != "A" {
		t.Fatalf("current source = %q, want A held isolated", coordinator.Current.Source)
	}
	if !coordinator.Current.Isolated() {
		t.Fatal("plant must be feed-isolated when neither source can be active")
	}
}

func TestSwitchIsolatesWhenNoPreviousSource(t *testing.T) {
	coordinator := NewCoordinator()
	coordinator.Available = func(source string) bool { return false }

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	var failure *SwitchError
	if !errors.As(err, &failure) {
		t.Fatalf("expected SwitchError, got %v", err)
	}
	if failure.Outcome != OutcomeIsolated {
		t.Fatalf("expected isolated outcome, got %q", failure.Outcome)
	}
	if !coordinator.Current.Isolated() {
		t.Fatal("plant must be feed-isolated")
	}
}
