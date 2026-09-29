package feed

import (
	"context"
	"errors"
	"github.com/wyw14/solo-039/internal/model"
)

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
	// Available 在动旧来源之前预检新来源是否可用。
	// nil 表示除空来源外全部可用（保持既有行为）。
	Available func(source string) bool
}

func NewCoordinator() *Coordinator { return &Coordinator{Current: &Route{}, Previous: &Route{}} }

// Switch 原子地从旧来源切到新来源：
//  1. 先预检新来源可用性（不可用则直接拒绝，旧来源和路由指针都不变）；
//  2. 隔离旧来源；
//  3. 激活新来源；若激活失败，重新打开旧来源并恢复路由指针，
//     使进料状态回到切换前，绝不把装置留在无进料状态。
func (c *Coordinator) Switch(ctx context.Context, source string, campaign model.Campaign) error {
	if err := c.checkAvailable(source); err != nil {
		return err
	}
	old := c.Current
	if old.Source != "" {
		if err := old.Close(ctx); err != nil {
			return err
		}
		c.emit("old-isolated", old, nil)
	}
	// 关旧之后、开新之前做第二次授权确认：来源可能在预检后被切除。
	next := &Route{}
	if c.Available != nil && !c.Available(source) {
		next.PrepareUnavailable(source)
	} else {
		next.Prepare(source)
	}
	if err := next.Activate(); err != nil {
		// 新来源不可用/激活失败：尝试回滚旧来源。
		if old.Source != "" {
			reopenErr := old.Reopen()
			// 路由字段只反映切换开始时的状态，重开后还要经注册表确认
			// 旧来源此刻仍投用；若旧来源也在切换窗口内被切除，则不能宣称已恢复。
			if reopenErr == nil && c.Available != nil && !c.Available(old.Source) {
				old.Isolate()
				reopenErr = ErrSourceUnavailable
			}
			if reopenErr != nil {
				// 旧来源也无法恢复：保持其隔离事实，交由上层进入安全停留。
				return errors.Join(err, reopenErr)
			}
			c.emit("old-restored", old, nil)
		}
		return err
	}
	c.Previous = old
	c.Current = next
	c.emit("new-active", old, c.Current)
	return nil
}

func (c *Coordinator) checkAvailable(source string) error {
	if source == "" {
		return errors.New("source required")
	}
	if c.Available != nil && !c.Available(source) {
		return ErrSourceUnavailable
	}
	return nil
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
