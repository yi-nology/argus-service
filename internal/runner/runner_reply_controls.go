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

// replyFilterStats 回复面过滤统计（v8.4.1）：报告尾注要如实告知静默了多少条、
// 为什么静默——v8.4.0 只落日志不落报告，「保留计数」的承诺在默认 standard
// 模板/analyst 模板下均未兑现（仅 minimal 自带 high/critical 封顶计数）。
type replyFilterStats struct {
	Floor int // 低于级别地板被静默的条数
	Cap   int // 超过数量上限被截断的条数
}

// Dropped 被静默的总条数。
func (s replyFilterStats) Dropped() int { return s.Floor + s.Cap }

// filterFindingsForReply 回复面过滤（数量 cap + 级别 floor）：
//   - floor 非空：地板以下 findings 静默（统计条数供报告尾注如实展示）；
//   - cap > 0：severity 降序取 top N（同级保持原顺序=稳定）；
//   - 返回新切片，不改输入（out.Merged.Findings 的其他消费面不受影响——
//     过滤只作用于「回复/渲染」出口）。
func filterFindingsForReply(in []domain.Finding, cap int, floor string) ([]domain.Finding, replyFilterStats) {
	var st replyFilterStats
	out := in
	if floor != "" {
		minRank := severityRank[floor]
		kept := make([]domain.Finding, 0, len(in))
		for _, f := range in {
			if severityRank[f.Severity] >= minRank {
				kept = append(kept, f)
			} else {
				st.Floor++
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
		st.Cap = len(out) - cap
		out = sorted[:cap]
	}
	return out, st
}

// recordReplyOutcome 回帖结果与静默计数落库（v8.5.0，best-effort）：
// 供 /stats/overview 聚合「旋钮生效证据」。失败只告警——审查/回帖结果不因
// 运营指标写入失败而回滚（与 checkCancelled 同款协作优化语义）。
func (r *Runner) recordReplyOutcome(ctx context.Context, task *storage.Task, state string, st replyFilterStats) {
	if r.Store == nil {
		return
	}
	if err := r.Store.SetTaskReplyOutcome(ctx, task.ID, state, st.Floor, st.Cap); err != nil {
		r.Log.Warn("argus.runner.reply_outcome_persist_failed",
			"task", task.ID, "state", state, "error", err.Error())
	}
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
