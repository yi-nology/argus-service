// runner_budget.go —— 执行前预算闸与预算拒绝收口（自 runner_lifecycle.go 拆出）。

package runner

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// budgetGate 执行前预算闸（v3.1/v6.9.0 双闸 → #32 预留判定单临界区 → Phase1①
// 授予值回传）：配额账（done 口径）与真实消费账（usage_event 实销）各自把
// 「他任务活跃预留」计入已承诺额度，本任务预留额度 = min(任务预算, 各自剩余额度)
// ——判定与落预留行在 storage.ReserveRepoBudget 的租约临界区内原子完成。
// 返回 (intercepted, granted)：intercepted=true 已走失败终态（granted 无意义）；
// 放行时 granted = 授予额度（≤0 = 无界/双账本关），调用方必须用它创建任务级
// LLM Budget——账面承诺与执行上限从此一致（Phase1① 修「DB 承诺 5,000 实跑
// 20,000」脱节）。语义对齐旧双闸：
//   - 任一账本剩余额度 ≤0 → failed 终态 + start marker + 播报（预算路径不重试）；
//   - 判定不可得（存储/租约故障）→ soft: fail-open 放行（历史语义）；
//     hard（ARGUS_BUDGET_ENFORCEMENT）: fail-closed 拒绝（Phase1①）；
//   - 双账本全关（budget<=0）→ 零开销直通不落预留，granted = 任务预算原值。
func (r *Runner) budgetGate(ctx context.Context, task *storage.Task, knobs taskKnobs, realBudget int) (bool, int64) {
	if knobs.monthlyBudget <= 0 && realBudget <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	now := time.Now()
	res, err := r.Store.ReserveRepoBudget(ctx, storage.ReserveInput{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
		TaskBudget:  knobs.taskTokenBudget,
		QuotaBudget: knobs.monthlyBudget, RealBudget: realBudget,
		Since: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	})
	if err != nil {
		r.Log.Warn("argus.runner.budget_reserve_failed", "task", task.ID, "error", err.Error())
		// Phase1①：hard 模式判定不可得 fail-closed——账本不可用就不开新计费
		// 调用；soft 保持历史 fail-open（可用性优先）。
		if r.Cfg != nil && r.Cfg.BudgetEnforcement == "hard" {
			r.rejectByBudget(ctx, task,
				// spec §2.1.3：reason 携带机器可读 token budget_ledger_unavailable
				//（runbook/告警可 grep）；中文文案与 markerTail 保持不变。
				"预算账本不可用（预留判定失败），ARGUS_BUDGET_ENFORCEMENT=hard 拒绝放行 [budget_ledger_unavailable]",
				"检查数据库与 leader_leases 后 push 新提交重试；可用性优先可切回 soft。")
			return true, 0
		}
		return false, int64(knobs.taskTokenBudget)
	}
	switch res.Verdict {
	case storage.ReserveQuotaExceeded:
		obs.IncrBudgetExceeded("quota")
		r.Log.Warn("argus.runner.repo_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.QuotaUsed, "budget", knobs.monthlyBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", res.QuotaUsed, knobs.monthlyBudget),
			"下月预算重置后 push 新提交可重新触发审查。")
		return true, 0
	case storage.ReserveRealExceeded:
		obs.IncrBudgetExceeded("real")
		r.Log.Warn("argus.runner.real_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.RealUsed, "budget", realBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月真实 token 实销+预留已达上限（used=%d, budget=%d）", res.RealUsed, realBudget),
			"下月重置后 push 新提交可重新触发审查；如需立即恢复请调整 ARGUS_REAL_TOKEN_BUDGET。")
		return true, 0
	}
	// 防御兜底：判定通过时授予值应 >0（任务预算非正的无界边界回落原值）。
	if res.GrantedTokens <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	return false, res.GrantedTokens
}

// rejectByBudget 预算拒绝统一收口（原 repoBudgetGate/realBudgetGate 失败体
// 合并，#32）：failed 终态（fenced）+ start marker 失败告知 + 终态播报 + 指标。
// markerTail 为两账本文案差分（真实账带 ARGUS_REAL_TOKEN_BUDGET 恢复提示）。
// tokens=0：拒绝发生在开跑前，本执行无消耗；预留行也不存在（仅判定通过才落）。
func (r *Runner) rejectByBudget(ctx context.Context, task *storage.Task, reason, markerTail string) {
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", reason, 0, 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"❌ Argus 审查失败：%s——%s\n\n<!-- argus:start task_id=%s -->",
		reason, markerTail, task.ID))
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, reason)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
}
