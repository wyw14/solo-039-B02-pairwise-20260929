package feed

import (
	"context"
	"errors"
	"github.com/wyw14/solo-039/internal/model"
	"testing"
)

func activate(t *testing.T, c *Coordinator, source string) {
	t.Helper()
	c.Current.Prepare(source)
	if err := c.Current.Activate(); err != nil {
		t.Fatal(err)
	}
}

func TestSwitchClosesPreviousRoute(t *testing.T) {
	coordinator := NewCoordinator()
	activate(t, coordinator, "A")
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

func TestSwitchRejectsUnavailableSourceBeforeTouchingOld(t *testing.T) {
	coordinator := NewCoordinator()
	coordinator.Available = func(source string) bool { return source == "A" }
	activate(t, coordinator, "A")
	oldRoute := coordinator.Current
	oldRevision := oldRoute.Revision

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("expected ErrSourceUnavailable, got %v", err)
	}
	// 旧来源、路由指针、修订号都必须保持原样。
	if coordinator.Current != oldRoute {
		t.Fatal("current route pointer changed despite rejected switch")
	}
	if !coordinator.Current.Active() || coordinator.Current.Source != "A" {
		t.Fatalf("old source disturbed: %+v", coordinator.Current)
	}
	if coordinator.Current.Revision != oldRevision {
		t.Fatal("old feed revision changed")
	}
	if coordinator.Previous.Source != "" {
		t.Fatal("previous route should stay empty")
	}
}

func TestSwitchRestoresOldSourceWhenNewActivationFails(t *testing.T) {
	coordinator := NewCoordinator()
	activate(t, coordinator, "A")

	// 预检通过，但关旧之后新来源被切除（TOCTOU），且旧来源仍可用。
	checks := 0
	coordinator.Available = func(source string) bool {
		checks++
		if source == "B" && checks == 1 {
			return true // 预检时还在
		}
		return source == "A" // 此后 B 不可用，A 仍投用
	}

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("expected activation failure, got %v", err)
	}
	if coordinator.Current.Source != "A" || !coordinator.Current.Active() {
		t.Fatalf("old source not restored: %+v", coordinator.Current)
	}
	if coordinator.Previous.Source != "" {
		t.Fatal("committed switch must not be visible after rollback")
	}
}

func TestSwitchSafeHoldWhenBothSourcesLost(t *testing.T) {
	coordinator := NewCoordinator()
	activate(t, coordinator, "A")

	// 预检通过；关旧之后 A、B 同时被切除。
	checks := 0
	coordinator.Available = func(source string) bool {
		checks++
		return checks == 1 // 仅第一次预检放行
	}

	err := coordinator.Switch(context.Background(), "B", model.NewCampaign("grade"))
	if !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("expected unavailability error, got %v", err)
	}
	if coordinator.Current.Active() {
		t.Fatal("no route may report active when both sources are lost")
	}
	if coordinator.Current.Source != "A" || !coordinator.Current.Isolated() {
		t.Fatalf("old route must remain isolated for safe hold, got %+v", coordinator.Current)
	}
}
