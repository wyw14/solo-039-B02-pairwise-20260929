package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/analyzer"
	"github.com/wyw14/solo-039/internal/campaign"
	"github.com/wyw14/solo-039/internal/column"
	"github.com/wyw14/solo-039/internal/feed"
	"github.com/wyw14/solo-039/internal/interlock"
	"github.com/wyw14/solo-039/internal/journal"
	"github.com/wyw14/solo-039/internal/model"
	"github.com/wyw14/solo-039/internal/product"
	"github.com/wyw14/solo-039/internal/reboiler"
	"github.com/wyw14/solo-039/internal/reflux"
	"github.com/wyw14/solo-039/internal/relief"
	"github.com/wyw14/solo-039/internal/utility"
	"path/filepath"
	"sync"
	"time"
)

type Plant struct {
	mu                sync.RWMutex
	Campaign          *campaign.State
	Column            *column.State
	Feed              *feed.Coordinator
	Reflux            *reflux.Coordinator
	RefluxState       *reflux.State
	Reboiler          *reboiler.State
	Analyzer          *analyzer.State
	Product           *product.Coordinator
	Interlock         *interlock.Coordinator
	Relief            *relief.Coordinator
	Capacity          *utility.Capacity
	Leases            *utility.LeaseBook
	Journal           *journal.Store
	FileLog           *journal.FileLog
	Snapshots         *journal.SnapshotFile
	FeedMetrics       *feed.Metrics
	RefluxTelemetry   *reflux.Telemetry
	ReboilerTelemetry *reboiler.Telemetry
	ProductAudit      *product.Audit
	UtilityLedger     *utility.Ledger
	Metrics           *Metrics
	CampaignHistory   *campaign.History
	Profile           *column.Profile
	RefluxAllocator   *reflux.Allocator
	ReboilerCommands  *reboiler.CommandBook
	Evidence          *analyzer.EvidenceCatalog
	ReliefHistory     *relief.History
	Incidents         *Incidents
	Sources           map[string]bool
	started           bool
	lastError         string
}

func NewPlant(dataDir string) *Plant {
	campaignState := campaign.New("baseline")
	analyzerState := analyzer.New()
	store := journal.New()
	refluxCoordinator := reflux.NewCoordinator(1)
	plant := &Plant{
		Campaign:          campaignState,
		Column:            column.New("T-101"),
		Feed:              feed.NewCoordinator(),
		Reflux:            refluxCoordinator,
		RefluxState:       reflux.New(),
		Reboiler:          reboiler.New(),
		Analyzer:          analyzerState,
		Product:           product.NewCoordinator(campaignState, analyzerState, store),
		Interlock:         interlock.NewCoordinator(),
		Relief:            relief.NewCoordinator(),
		Capacity:          utility.NewCapacity(1),
		Leases:            utility.NewLeaseBook(),
		Journal:           store,
		FileLog:           journal.NewFileLog(filepath.Join(dataDir, "events.jsonl")),
		Snapshots:         journal.NewSnapshotFile(filepath.Join(dataDir, "snapshot.json")),
		FeedMetrics:       feed.NewMetrics(),
		RefluxTelemetry:   reflux.NewTelemetry(),
		ReboilerTelemetry: reboiler.NewTelemetry(),
		ProductAudit:      product.NewAudit(),
		UtilityLedger:     utility.NewLedger(),
		Metrics:           NewMetrics(),
		CampaignHistory:   campaign.NewHistory(),
		Profile:           column.NewProfile(),
		RefluxAllocator:   reflux.NewAllocator(refluxCoordinator),
		ReboilerCommands:  reboiler.NewCommandBook(),
		Evidence:          analyzer.NewEvidenceCatalog(),
		ReliefHistory:     relief.NewHistory(),
		Incidents:         NewIncidents(),
		Sources:           map[string]bool{"primary": true},
	}
	plant.Reflux.Register("T-101", plant.RefluxState)
	plant.Feed.Observe = plant.observeFeed
	plant.Feed.Available = func(source string) bool { return plant.Sources[source] }
	return plant
}

// SetSourceAvailable 登记/撤销一个进料来源的可用性。
// 撤销一个已投用来源前应先完成换品级；不可用来源会被换品级预检拒绝。
func (p *Plant) SetSourceAvailable(source string, available bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if available {
		p.Sources[source] = true
	} else {
		delete(p.Sources, source)
	}
}

func (p *Plant) Start(ctx context.Context, grade, source string) (model.Campaign, error) {
	var operationErr error
	defer func() { p.Metrics.Record("start", operationErr) }()
	if err := model.ValidateGrade(grade); err != nil {
		operationErr = err
		return model.Campaign{}, err
	}
	p.mu.Lock()
	if p.started {
		current := p.Campaign.Snapshot()
		p.mu.Unlock()
		return current, errors.New("plant already started")
	}
	p.started = true
	p.lastError = ""
	p.mu.Unlock()
	before := p.Campaign.Snapshot()
	// 先切进料（含可用性预检与失败回滚），成功后再提交开车批次的品级，
	// 避免进料未建立却已改换批次品级。
	if err := p.Feed.Switch(ctx, source, before); err != nil {
		operationErr = err
		p.fail(err)
		p.mu.Lock()
		p.started = false
		p.mu.Unlock()
		return model.Campaign{}, p.snapshotFailure(StageFeedSwitch, true, false, retryStartFeed, err)
	}
	current := before
	current.Grade = grade
	campaign.Restore(p.Campaign, current)
	p.CampaignHistory.Record(before, current, "start grade selected")
	p.Column.SetFeedRevision(p.Feed.Current.Revision)
	p.Analyzer.BindFeed(p.Feed.Current.Revision)
	p.Campaign.Advance(model.Warming)
	if err := p.Reboiler.Ramp(ctx, p.Campaign.Snapshot(), 0.5); err != nil {
		operationErr = err
		p.fail(err)
		return model.Campaign{}, err
	}
	p.Campaign.Advance(model.Stabilizing)
	p.Column.Apply(p.Campaign.Snapshot(), 1.1, 0.5)
	_ = p.Profile.Put(column.TrayReading{Tray: 1, Temperature: 118, Pressure: 1.1})
	_ = p.Profile.Put(column.TrayReading{Tray: 20, Temperature: 82, Pressure: 0.9})
	p.ReboilerTelemetry.Record(p.Reboiler.Snapshot())
	if err := p.record("plant.started", source); err != nil {
		p.fail(err)
		operationErr = err
		return model.Campaign{}, err
	}
	return p.Campaign.Snapshot(), nil
}

func (p *Plant) Transition(ctx context.Context, grade, source string) (model.Campaign, error) {
	if err := model.ValidateGrade(grade); err != nil {
		return model.Campaign{}, p.snapshotFailure(StageValidate, true, false, retryValidate, err)
	}
	before := p.Campaign.Snapshot()
	// 第一步：在提交任何批次修订之前先切进料。
	// feed.Switch 自身具备原子性：新来源预检失败时不动旧来源；
	// 关旧后新源激活失败时会重开旧源。因此进料要么完整切到新源，要么留在旧源。
	if err := p.Feed.Switch(ctx, source, before); err != nil {
		p.fail(err)
		if p.Feed.Current.Active() {
			// 旧来源已自动重开：批次修订尚未提交、进料留在原来源，结论一致。
			return model.Campaign{}, p.snapshotFailure(StageFeedSwitch, true, false, retryFeedSwitch, err)
		}
		// 新旧来源都不可用：装置无进料，进入 transitioning 安全停留并切断蒸汽，
		// 绝不在无进料的情况下继续按原批次生产。
		hold := p.Campaign.Advance(model.Transitioning)
		p.Column.Apply(hold, p.Column.Snapshot().Pressure, p.Column.Snapshot().Level)
		if closeErr := p.ReboilerCommands.Close(ctx, p.Reboiler, hold); closeErr != nil {
			// 指令通道也失败时至少就地停止再沸器控制环。
			p.Reboiler.Stop()
			err = errors.Join(err, closeErr)
		}
		p.ReboilerTelemetry.Record(p.Reboiler.Snapshot())
		p.recordSafeHold(StageFeedSwitch)
		return model.Campaign{}, p.snapshotFailure(StageFeedSwitch, false, true, retrySafeHold, err)
	}

	// 第二步：进料已切到新源，提交批次修订（新世代）。
	current := p.Campaign.Advance(model.Transitioning)
	current.Grade = grade
	current.Revision = p.Feed.Current.Revision
	campaign.Restore(p.Campaign, current)
	p.CampaignHistory.Record(before, current, "grade transition")
	p.Column.SetFeedRevision(p.Feed.Current.Revision)
	p.Analyzer.BindFeed(p.Feed.Current.Revision)

	// 第三步：持久化。若持久化失败，进料与批次已在新世代一致，
	// 但事件未落盘——不能静默回滚（否则内存与现场进料相反），进入安全停留。
	if err := p.record("campaign.transition", grade); err != nil {
		p.fail(err)
		p.recordSafeHold(StageRecord)
		return model.Campaign{}, p.snapshotFailure(StageRecord, false, true, retrySafeHold, err)
	}
	return p.Campaign.Snapshot(), nil
}

func (p *Plant) AddSample(value float64, at int64) (int, error) {
	revision := p.Feed.Current.Revision
	calibration := p.Analyzer.Calibration()
	ok := p.Analyzer.Add(model.Sample{ID: uuid.New(), FeedRevision: revision, Calibration: calibration, At: at, Component: "top", Value: value, Valid: true})
	if !ok {
		return p.Analyzer.SampleCount(), errors.New("sample rejected by current evidence generation")
	}
	return p.Analyzer.SampleCount(), nil
}

func (p *Plant) CalibrateAnalyzer() uuid.UUID {
	revision := p.Analyzer.Calibrate()
	p.Analyzer.BindFeed(p.Feed.Current.Revision)
	return revision
}

func (p *Plant) RouteProduct(target string) error {
	current := p.Campaign.Snapshot()
	proof, ok := p.Analyzer.Proof(current.Revision)
	if !ok {
		return errors.New("composition evidence is incomplete")
	}
	_ = p.Evidence.Add(proof)
	pressureProof := model.Proof{ID: uuid.New(), Revision: current.Revision, Source: "column-pressure", Digest: fmt.Sprintf("%.3f", p.Column.Snapshot().Pressure), Durable: true, Independent: true, At: time.Now().Unix()}
	permit := p.Interlock.Evaluate([]model.Proof{proof, pressureProof}, model.Campaign{ID: current.ID, Revision: current.Revision, Phase: model.OnSpec, Epoch: current.Epoch, Fencing: current.Fencing})
	if err := permit.Error(); err != nil {
		return err
	}
	current.Phase = model.OnSpec
	campaign.Restore(p.Campaign, current)
	plan := model.ProductPlan{Campaign: current.ID, Revision: current.Revision, Grade: current.Grade, Target: target}
	if err := p.Product.PublishWithProof(plan, proof); err != nil {
		p.ProductAudit.Record(plan, proof, false, err.Error())
		p.fail(err)
		return err
	}
	p.ProductAudit.Record(plan, proof, true, "durable proof accepted")
	return p.record("product.routed", target)
}

func (p *Plant) AcquireReflux(owner string, amount float64) (model.Lease, error) {
	current := p.Campaign.Snapshot()
	lease, err := p.Reflux.Acquire(owner, amount, current.Fencing)
	if err != nil {
		p.Incidents.Open("reflux-capacity", err.Error(), current.Revision.String())
		return model.Lease{}, err
	}
	p.Leases.Put(lease)
	p.UtilityLedger.Commit(lease)
	p.RefluxTelemetry.Record(p.RefluxState.Snapshot())
	if err := p.record("reflux.lease", owner); err != nil {
		p.Reflux.Release(lease)
		return model.Lease{}, err
	}
	return lease, nil
}

func (p *Plant) Shutdown(ctx context.Context, reliefTimeout bool) (relief.ShutdownResult, error) {
	current := p.Campaign.Advance(model.Shutting)
	reliefErr := p.Relief.Shutdown(ctx, current, "flare", reliefTimeout)
	steamErr := p.ReboilerCommands.Close(ctx, p.Reboiler, current)
	p.ReboilerTelemetry.Record(p.Reboiler.Snapshot())
	result := relief.CombineShutdown(reliefErr, steamErr, p.Relief.State.Snapshot(), p.Reboiler.ClosureConfirmed(current.Epoch))
	p.ReliefHistory.Record(p.Relief.State.Snapshot(), reliefErr)
	if err := result.Error(); err != nil {
		p.Incidents.Open("shutdown", err.Error(), current.Revision.String())
		p.fail(err)
		return result, err
	}
	p.Campaign.Stop()
	p.mu.Lock()
	p.started = false
	p.mu.Unlock()
	return result, p.record("plant.stopped", "safe")
}

func (p *Plant) Snapshot() model.Snapshot {
	return model.Snapshot{Campaign: p.Campaign.Snapshot(), Column: p.Column.Snapshot(), Reflux: p.RefluxState.Snapshot(), Reboiler: p.Reboiler.Snapshot(), Products: p.ProductTargets(), Relief: p.Relief.State.Snapshot()}
}

func (p *Plant) Save() error { return p.Snapshots.Save(p.Snapshot()) }

func (p *Plant) Restore() error {
	snapshot, err := p.Snapshots.Load()
	if err != nil {
		return err
	}
	campaign.Restore(p.Campaign, snapshot.Campaign)
	column.Restore(p.Column, snapshot.Column)
	if err := reboiler.Restore(p.Reboiler, snapshot.Reboiler, snapshot.Campaign); err != nil {
		return err
	}
	if err := journal.ValidateRecovery(snapshot, p.Journal.Events()); err != nil {
		return err
	}
	p.mu.Lock()
	p.started = snapshot.Campaign.Phase != model.Stopped
	p.mu.Unlock()
	if !model.PhaseAllowsHeat(snapshot.Campaign.Phase) {
		p.Reboiler.Stop()
	}
	return nil
}

func (p *Plant) ProductTargets() []string {
	if p.Product.Route.Opened() {
		return []string{p.Product.Route.Target()}
	}
	return []string{}
}

func (p *Plant) Status() map[string]any {
	p.mu.RLock()
	started := p.started
	lastError := p.lastError
	p.mu.RUnlock()
	gradient, _ := p.Profile.Gradient()
	return map[string]any{"started": started, "last_error": lastError, "campaign": p.Campaign.Snapshot(), "column": p.Column.Snapshot(), "reflux_used": p.Reflux.Used(), "products": p.ProductTargets(), "relief": p.Relief.State.Snapshot(), "feed_metrics": p.FeedMetrics.Snapshot(), "reflux_telemetry": p.RefluxTelemetry.Snapshot(), "reboiler_telemetry": p.ReboilerTelemetry.Snapshot(), "product_decisions": p.ProductAudit.Count(), "utility_used": p.UtilityLedger.Used(), "metrics": p.Metrics.Snapshot(), "campaign_changes": p.CampaignHistory.Count(), "profile_gradient": gradient, "profile_stable": p.Profile.Stable(40), "evidence_count": p.Evidence.Count(p.Campaign.Snapshot().Revision.String()), "relief_attempts": p.ReliefHistory.Count(), "open_incidents": p.Incidents.CountOpen(), "operational_audit": p.OperationalAudit()}
}

func (p *Plant) record(kind, payload string) error {
	event := model.NewEvent(kind, p.Campaign.Snapshot(), payload)
	event.At = time.Now().UnixNano()
	if err := p.Journal.Append(event); err != nil {
		return err
	}
	if err := p.FileLog.Append(event); err != nil {
		return err
	}
	return p.Save()
}

func (p *Plant) observeFeed(value feed.Transition) {
	// 切换过程中的事件不落盘：进料切换是批次修订的一部分，
	// 持久化由批次提交（campaign.transition/plant.started）统一完成，
	// 避免在批次修订尚未提交时写入指向新进料世代的中间事件。
	if value.Stage == "new-active" {
		p.FeedMetrics.Record(value.NewSource)
	}
}

func (p *Plant) fail(err error) {
	p.mu.Lock()
	p.lastError = err.Error()
	p.mu.Unlock()
	p.Incidents.Open("runtime", err.Error(), p.Campaign.Snapshot().Revision.String())
}

// recordSafeHold 在进入安全停留时 best-effort 落盘一条中止事件并刷新快照，
// 保证重启恢复到的批次/进料状态与现场一致；落盘失败只登记事故，不掩盖原始结论。
func (p *Plant) recordSafeHold(stage string) {
	if err := p.record("campaign.transition.aborted", stage); err != nil {
		p.fail(err)
	}
}
