// runner_reply_controls.go —— 仓级回复控制三旋钮消费点（v8.4.0）：
// findings cap/floor 过滤（回复数量/级别）+ throttle 门控（回复频次）。
// 旋钮来源 taskKnobs（repo_settings 按仓覆盖，admin API 写入的可信面）。

package runner

import (
	"context"
	"sort"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

// severityRank 级别降序权重（critical 最高）。未知级别按最低处理（宁静默
// 不误报高档——渲染面另有 unknown 计数）。
var severityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}

// filterFindingsForReply 回复面过滤（数量 cap + 级别 floor）：
//   - floor 非空：地板以下 findings 静默（报告只留计数行，渲染面已有该形态）；
//   - cap > 0：severity 降序取 top N（同级保持原顺序=稳定）；
//   - 返回新切片，不改输入（out.Merged.Findings 的其他消费面不受影响——
//     过滤只作用于「回复/渲染」出口）。
func filterFindingsForReply(in []domain.Finding, cap int, floor string) []domain.Finding {
	out := in
	if floor != "" {
		minRank := severityRank[floor]
		kept := make([]domain.Finding, 0, len(in))
		for _, f := range in {
			if severityRank[f.Severity] >= minRank {
				kept = append(kept, f)
			}
		}
		out = kept
	}
	if cap > 0 && len(out) > cap {
		sorted := make([]domain.Finding, len(out))
		copy(sorted, out)
		// 稳定排序：severity 降序，同级保原序
		sort.SliceStable(sorted, func(i, j int) bool {
			return severityRank[sorted[i].Severity] > severityRank[sorted[j].Severity]
		})
		out = sorted[:cap]
	}
	return out
}

// replyThrottled 回复频次门控（throttle 命中=true 跳过本轮回帖，任务照常
// done、报告落库）：同 PR 上次 done 完成距今 < throttle 窗口。查询失败
// fail-open（不节流——与 checkCancelled 同款「协作优化不是正确性防线」语义）。
func (r *Runner) replyThrottled(ctx context.Context, task *storage.Task, knobs taskKnobs) bool {
	if knobs.commentThrottleMin <= 0 || r.Store == nil {
		return false
	}
	fin, ok, err := r.Store.LastDoneTaskFinishedAt(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		r.Log.Warn("argus.runner.reply_throttle_check_failed", "task", task.ID, "error", err.Error())
		return false
	}
	if !ok {
		return false
	}
	return time.Since(fin) < time.Duration(knobs.commentThrottleMin)*time.Minute
}
