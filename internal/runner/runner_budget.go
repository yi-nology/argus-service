// runner_budget.go —— 执行前预算闸与预算拒绝收口（自 runner_lifecycle.go 拆出）。

package runner

import (
	"context"

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
	return budgetGate(r, ctx, task, knobs, realBudget)
}

// rejectByBudget 预算拒绝统一收口（原 repoBudgetGate/realBudgetGate 失败体
// 合并，#32）：failed 终态（fenced）+ start marker 失败告知 + 终态播报 + 指标。
// markerTail 为两账本文案差分（真实账带 ARGUS_REAL_TOKEN_BUDGET 恢复提示）。
// tokens=0：拒绝发生在开跑前，本执行无消耗；预留行也不存在（仅判定通过才落）。
func (r *Runner) rejectByBudget(ctx context.Context, task *storage.Task, reason, markerTail string) {
	rejectByBudget(r, ctx, task, reason, markerTail)
}
