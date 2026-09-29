package service

import (
	"context"
	"errors"
	"testing"

	"github.com/wyw14/solo-039/internal/journal"
	"github.com/wyw14/solo-039/internal/model"
)

func startedPlant(t *testing.T, dir string) *Plant {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	plant := NewPlant(dir)
	if _, err := plant.Start(context.Background(), "baseline", "primary"); err != nil {
		t.Fatalf("start: %v", err)
	}
	return plant
}

func assertRestoresToHold(t *testing.T, dir string, grade string) {
	t.Helper()
	reloaded := NewPlant(dir)
	if err := reloaded.Restore(); err != nil {
		t.Fatalf("restore from safe hold failed: %v", err)
	}
	snap := reloaded.Campaign.Snapshot()
	if snap.Phase != model.Transitioning || snap.Grade != grade {
		t.Fatalf("restored state not a safe hold: phase=%s grade=%s", snap.Phase, snap.Grade)
	}
}

func asTransitionFailure(t *testing.T, err error) *TransitionFailure {
	t.Helper()
	var failure *TransitionFailure
	if !errors.As(err, &failure) {
		t.Fatalf("expected *TransitionFailure, got %T (%v)", err, err)
	}
	return failure
}

func TestTransitionUnavailableSourceRollsBackConsistently(t *testing.T) {
	plant := startedPlant(t, "")
	before := plant.Campaign.Snapshot()
	columnFeedRevision := plant.Column.Snapshot().FeedRevision

	_, err := plant.Transition(context.Background(), "premium", "secondary")
	failure := asTransitionFailure(t, err)

	if failure.FailedStage != StageFeedSwitch || !failure.RolledBack || failure.SafeHold {
		t.Fatalf("expected rollback at feed-switch, got %+v", failure)
	}
	if failure.RetryFrom == "" {
		t.Fatal("operator must be told where to retry")
	}
	// 批次仍是原品级/原修订/原相位。
	after := plant.Campaign.Snapshot()
	if after.Grade != "baseline" || after.Revision != before.Revision || after.Phase != model.Stabilizing {
		t.Fatalf("campaign changed after failed switch: %+v", after)
	}
	// 进料仍是原来源且在投用；塔绑定的进料修订未变。
	if plant.Feed.Current.Source != "primary" || !plant.Feed.Current.Active() {
		t.Fatalf("feed not restored: %+v", plant.Feed.Current)
	}
	if plant.Column.Snapshot().FeedRevision != columnFeedRevision {
		t.Fatal("column feed revision changed despite rollback")
	}
	// 失败期间不得写入半成品事件；事件序列与快照恢复校验仍通过。
	if err := journal.ValidateSequence(plant.Journal.Events()); err != nil {
		t.Fatalf("journal sequence broken: %v", err)
	}
	if err := journal.ValidateRecovery(plant.Snapshot(), plant.Journal.Events()); err != nil {
		t.Fatalf("recovery validation broken: %v", err)
	}

	// 操作员按指引从「切换进料来源」重试：来源投用后一次成功。
	plant.SetSourceAvailable("secondary", true)
	completed, err := plant.Transition(context.Background(), "premium", "secondary")
	if err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if completed.Grade != "premium" || completed.Phase != model.Transitioning {
		t.Fatalf("unexpected campaign after retry: %+v", completed)
	}
	if plant.Feed.Current.Source != "secondary" || !plant.Feed.Current.Active() {
		t.Fatalf("feed not switched on retry: %+v", plant.Feed.Current)
	}
	if completed.Revision != plant.Feed.Current.Revision {
		t.Fatal("campaign revision must match active feed revision")
	}
	if err := journal.ValidateRecovery(plant.Snapshot(), plant.Journal.Events()); err != nil {
		t.Fatalf("recovery validation broken after retry: %v", err)
	}
}

func TestTransitionSafeHoldWhenBothSourcesLost(t *testing.T) {
	dir := t.TempDir()
	plant := startedPlant(t, dir)

	// 预检放行 secondary；关旧之后，新、旧来源同时不可用。
	checks := 0
	plant.Feed.Available = func(source string) bool {
		checks++
		if checks == 1 {
			return true
		}
		delete(plant.Sources, "primary")
		return false
	}

	_, err := plant.Transition(context.Background(), "premium", "secondary")
	failure := asTransitionFailure(t, err)
	if failure.FailedStage != StageFeedSwitch || failure.RolledBack || !failure.SafeHold {
		t.Fatalf("expected safe hold at feed-switch, got %+v", failure)
	}
	if plant.Campaign.Snapshot().Phase != model.Transitioning {
		t.Fatalf("campaign must be in transitioning hold, got %s", plant.Campaign.Snapshot().Phase)
	}
	if plant.Feed.Current.Active() {
		t.Fatal("no feed source may report active in safe hold")
	}
	if !plant.Reboiler.Snapshot().Closed {
		t.Fatal("heat must be removed in safe hold")
	}
	if !plant.Reboiler.ClosureConfirmed(plant.Campaign.Snapshot().Epoch) {
		t.Fatal("steam closure must be confirmed against the hold epoch")
	}
	// 停留态必须已落盘：新进程恢复后仍是同一个一致的 transitioning 停留结论。
	assertRestoresToHold(t, dir, "baseline")

	// 排除故障（旧源重新投用、新源可用）后，按指引从切换步骤重试成功。
	plant.SetSourceAvailable("primary", true)
	plant.SetSourceAvailable("secondary", true)
	plant.Feed.Available = func(source string) bool { return plant.Sources[source] }
	completed, err := plant.Transition(context.Background(), "premium", "secondary")
	if err != nil {
		t.Fatalf("retry from safe hold failed: %v", err)
	}
	if completed.Grade != "premium" || plant.Feed.Current.Source != "secondary" {
		t.Fatalf("retry did not complete the grade switch: %+v", completed)
	}
}

func TestTransitionSafeHoldWhenPersistFails(t *testing.T) {
	dir := t.TempDir()
	plant := startedPlant(t, dir)
	plant.SetSourceAvailable("secondary", true)
	plant.Journal.FailNext()

	_, err := plant.Transition(context.Background(), "premium", "secondary")
	failure := asTransitionFailure(t, err)
	if failure.FailedStage != StageRecord || !failure.SafeHold {
		t.Fatalf("expected safe hold at persist, got %+v", failure)
	}
	// 现场已切到新源：批次与进料在新世代保持一致，不能静默回滚。
	current := plant.Campaign.Snapshot()
	if current.Grade != "premium" || current.Phase != model.Transitioning {
		t.Fatalf("unexpected campaign in hold: %+v", current)
	}
	if plant.Feed.Current.Source != "secondary" || !plant.Feed.Current.Active() {
		t.Fatalf("physical feed must follow committed campaign: %+v", plant.Feed.Current)
	}
	// 停留态已 best-effort 落盘：重启后恢复为同一个新世代停留结论。
	assertRestoresToHold(t, dir, "premium")

	// 重试后事件落盘，快照与事件序列重新一致。
	if _, err := plant.Transition(context.Background(), "premium", "secondary"); err != nil {
		t.Fatalf("retry after persist failure failed: %v", err)
	}
	if err := journal.ValidateSequence(plant.Journal.Events()); err != nil {
		t.Fatalf("journal sequence broken: %v", err)
	}
	if err := journal.ValidateRecovery(plant.Snapshot(), plant.Journal.Events()); err != nil {
		t.Fatalf("recovery validation broken: %v", err)
	}
}

func TestStartUnavailableSourceDoesNotCommitCampaign(t *testing.T) {
	plant := NewPlant(t.TempDir())
	_, err := plant.Start(context.Background(), "special", "secondary")
	failure := asTransitionFailure(t, err)
	if !failure.RolledBack || failure.FailedStage != StageFeedSwitch {
		t.Fatalf("expected rollback, got %+v", failure)
	}
	if plant.Campaign.Snapshot().Grade != "baseline" {
		t.Fatal("grade must not be committed before feed is established")
	}
	if !plant.Feed.Current.Isolated() {
		t.Fatal("no feed may be active after failed start")
	}
	if plant.started {
		t.Fatal("plant must not report started after a failed start")
	}
}
