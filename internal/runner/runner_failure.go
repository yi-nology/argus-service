// runner_failure.go —— 任务失败收尾（自 runner_lifecycle.go 拆出）。

package runner

import (
	"context"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/storage"
)

// handleRunFailure 任务失败收尾：停机硬取消显式保持 running（依赖心跳过期重置
// 回 pending，此前靠"ctx 已死 → 写库必然失败"的副作用成立，语义脆弱已改）；
// 其余落 failed 终态（fenced）+ PR 侧失败告知（未超重试上限=中断将自动重试，
// 超限/禁用=最终失败，v3.30.1：失败必须显式告之，不许 MR 作者只见「审查中」
// 挂死）+ 终态播报。错误链可能携带带凭据 URL，三路消费统一先脱敏（logredact）。
func (r *Runner) handleRunFailure(ctx context.Context, task *storage.Task, budget *llm.Budget, runErr error) {
	handleRunFailure(r, ctx, task, budget, runErr)
}
