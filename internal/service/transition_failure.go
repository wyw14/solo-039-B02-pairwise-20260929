package service

import (
	"fmt"

	"github.com/wyw14/solo-039/internal/model"
)

// 换品级操作各步骤的稳定标识，供操作员定位重试入口。
const (
	StageValidate   = "validate-request"
	StageFeedSwitch = "feed-switch"
	StageRecord     = "persist-transition"
	retryValidate   = "重新发起换品级请求（更正品级/来源参数后）"
	retryFeedSwitch = "在当前批次（原品级）下重新发起换品级，从「切换进料来源」步骤重试"
	retrySafeHold   = "先排除故障，然后从「重新切换进料来源」步骤重试；在此之前装置停留在 transitioning 安全状态，请勿当作换品完成"
	retryStartFeed  = "在当前批次下重新发起开车，从「打开进料」步骤重试"
)

// TransitionFailure 是换品级/开车失败时返回给操作员的结构化结论。
// 它明确回答三个问题：卡在哪一步、批次与进料最终停在什么状态、应从哪一步重试。
type TransitionFailure struct {
	// FailedStage 是首个失败的步骤标识。
	FailedStage string
	// RolledBack 为 true 表示已回退到操作前的原批次/原进料（完全一致结论）。
	RolledBack bool
	// SafeHold 为 true 表示无法自动回滚，装置停留在一致的安全状态。
	SafeHold bool
	// RetryFrom 是面向操作员的重试指引（中文）。
	RetryFrom string
	// Grade/Revision/Phase/FeedSource/FeedActive 是失败后的最终状态快照。
	Grade      string
	Revision   string
	Phase      model.Phase
	FeedSource string
	FeedActive bool
	Cause      error
}

func (f *TransitionFailure) Error() string {
	feedSource := f.FeedSource
	if feedSource == "" {
		feedSource = "无进料"
	}
	switch {
	case f.RolledBack:
		return fmt.Sprintf("换品级在「%s」步骤失败，已回退到原批次（品级 %s，进料来源 %s）。%s",
			f.FailedStage, f.Grade, feedSource, f.RetryFrom)
	case f.SafeHold:
		return fmt.Sprintf("换品级在「%s」步骤失败且无法自动回退，装置安全停留在 %s 相位（品级 %s，进料来源 %s）。%s",
			f.FailedStage, f.Phase, f.Grade, feedSource, f.RetryFrom)
	default:
		return fmt.Sprintf("换品级在「%s」步骤失败：%v。%s", f.FailedStage, f.Cause, f.RetryFrom)
	}
}

func (f *TransitionFailure) Unwrap() error { return f.Cause }

// snapshotFailure 汇总失败后批次与进料的最终一致状态。
func (p *Plant) snapshotFailure(stage string, rolledBack, safeHold bool, retryFrom string, cause error) *TransitionFailure {
	campaignSnap := p.Campaign.Snapshot()
	return &TransitionFailure{
		FailedStage: stage,
		RolledBack:  rolledBack,
		SafeHold:    safeHold,
		RetryFrom:   retryFrom,
		Grade:       campaignSnap.Grade,
		Revision:    campaignSnap.Revision.String(),
		Phase:       campaignSnap.Phase,
		FeedSource:  p.Feed.Current.Source,
		FeedActive:  p.Feed.Current.Active(),
		Cause:       cause,
	}
}
