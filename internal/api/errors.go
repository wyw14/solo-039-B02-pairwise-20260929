package api

import (
	"errors"
	"net/http"

	"github.com/wyw14/solo-039/internal/service"
)

// writeTransitionError 把换品级/开车失败的结构化结论返回给操作员，
// 明确卡在哪一步、是否已回退/安全停留、当前批次与进料状态、应从哪一步重试。
func writeTransitionError(w http.ResponseWriter, err error) {
	var failure *service.TransitionFailure
	if errors.As(err, &failure) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":        failure.Error(),
			"failed_stage": failure.FailedStage,
			"rolled_back":  failure.RolledBack,
			"safe_hold":    failure.SafeHold,
			"retry_from":   failure.RetryFrom,
			"campaign": map[string]any{
				"grade":    failure.Grade,
				"revision": failure.Revision,
				"phase":    failure.Phase,
			},
			"feed": map[string]any{
				"source": failure.FeedSource,
				"active": failure.FeedActive,
			},
		})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
}
