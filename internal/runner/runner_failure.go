// runner_failure.go —— 任务失败收尾（自 runner_lifecycle.go 拆出）。

package runner

import (
	"context"
	"fmt"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// handleRunFailure 任务失败收尾：停机硬取消显式保持 running（依赖心跳过期重置
// 回 pending，此前靠"ctx 已死 → 写库必然失败"的副作用成立，语义脆弱已改）；
// 其余落 failed 终态（fenced）+ PR 侧失败告知（未超重试上限=中断将自动重试，
// 超限/禁用=最终失败，v3.30.1：失败必须显式告之，不许 MR 作者只见「审查中」
// 挂死）+ 终态播报。错误链可能携带带凭据 URL，三路消费统一先脱敏（logredact）。
func (r *Runner) handleRunFailure(ctx context.Context, task *storage.Task, budget *llm.Budget, runErr error) {
	if ctx.Err() != nil {
		r.Log.Warn("argus.runner.task_cancelled", "task", task.ID,
			"action", "保持 running，待心跳过期重置回 pending")
		return
	}
	errMsg := logredact.Redact(runErr.Error())
	r.Log.Error("argus.runner.task_failed", "task", task.ID, "error", errMsg)
	used := 0
	if budget != nil { // Phase1①：panic 可能发生在 Budget 创建前
		used = budget.Used()
	}
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", errMsg, used, 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
	} else if fenced {
		// stale 重置后新执行体已接管：本次写入归属失效，属预期而非错误
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	// PR 侧失败告知（best-effort，平台不可达时静默放弃）：failed-retry 扫
	// 5m×3^n 退避后重排。
	if cur, gerr := r.Store.GetTask(ctx, task.ID); gerr == nil {
		var body string
		switch {
		case r.Cfg.TaskMaxRetries > 0 && cur.Attempt < r.Cfg.TaskMaxRetries:
			body = fmt.Sprintf("⚠️ Argus 审查中断（第 %d 次尝试失败），数分钟后将自动重试。\n> %s\n\n<!-- argus:start task_id=%s -->",
				cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
		default:
			body = fmt.Sprintf("❌ Argus 审查失败（已尝试 %d 次）：\n> %s\n\npush 新提交可重新触发审查。\n\n<!-- argus:start task_id=%s -->",
				cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
		}
		r.setStartMarker(ctx, task, body)
	} else {
		r.Log.Warn("argus.runner.fail_notify_skip", "task", task.ID, "error", gerr.Error())
	}
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, errMsg)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
}
