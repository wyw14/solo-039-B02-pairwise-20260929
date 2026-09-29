package feed

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/wyw14/solo-039/internal/model"
)

// ErrSourceUnavailable 表示请求的进料来源当前不可用（未投用/已切除）。
// 该错误必须在触碰旧来源之前就可被识别，避免把装置留在无进料状态。
var ErrSourceUnavailable = errors.New("feed source is unavailable")

type Route struct {
	Source    string
	Revision  uuid.UUID
	Open      bool
	Flow      float64
	available bool
}

func (r *Route) Prepare(source string) { r.prepare(source, true) }

// PrepareUnavailable 构造一个已知不可用的路由，用于模拟/标记未投用来源。
func (r *Route) PrepareUnavailable(source string) { r.prepare(source, false) }

func (r *Route) prepare(source string, available bool) {
	r.Source = source
	r.Revision = uuid.New()
	r.Open = false
	r.Flow = 0
	r.available = available
}

func (r *Route) Available() bool { return r.available }

func (r *Route) Isolated() bool { return !r.Open && r.Flow == 0 }

// Isolate 立即切断该路由（用于确认到来源已被切除时如实反映现场）。
func (r *Route) Isolate() { r.Open = false; r.Flow = 0 }

func (r *Route) Active() bool { return r.Open && r.Flow > 0 }

func (r *Route) Activate() error {
	if r.Source == "" {
		return errors.New("source required")
	}
	if !r.available {
		return ErrSourceUnavailable
	}
	r.Open = true
	r.Flow = 1
	return nil
}

// Reopen 在切换失败后恢复先前在用的来源，使进料状态回到切换前。
func (r *Route) Reopen() error {
	if r.Source == "" {
		return errors.New("source required")
	}
	if !r.available {
		return ErrSourceUnavailable
	}
	r.Open = true
	r.Flow = 1
	return nil
}

func (r *Route) Close(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		r.Open = false
		r.Flow = 0
		return nil
	}
}

func (r *Route) Event(c model.Campaign) model.Event { return model.NewEvent("feed.route", c, r.Source) }
