// workflow_dispatch.go —— R4 专家委派：路由、预算裁剪、fan-out、共识、
// SAST 并入与 G4 受限补审。

package requirement

import (
	"context"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

// dispatchR4 R4 专家委派：确定性路由 → 预算裁剪 → fan-out → 编队共识 → SAST
// 并入 → G4 受限补审。返回裁决所需的全量产物（sel 供完备性裁判、retryNotes
// 供降级注明）。
func (w *Workflow) dispatchR4(ctx context.Context, task plugin.Task, budget *llm.Budget,
	rc *plugin.RepoConfig, changedFiles, skills []string) (sel []dispatch.Selection,
	outcomes []dispatch.Outcome, pruned, retryNotes []string) {
	return dispatchR4(w, ctx, task, budget, rc, changedFiles, skills)
}

// retryFailedPlugins G4 受限补审：对执行失败（Err 非空且非任务取消跳过）的插件
// 重试一次。约束：剩余预算 >0 才补审；重试经 Dispatcher.Run 照常过预算门/熔断门
// （已熔断插件会被跳过，保留原错误）；重试成功**替换**原 outcome——不叠加 findings，
// 不产生双重指纹。consensus 产物不重试（黑板轮转语义由 RunConsensus 自管）。
func (w *Workflow) retryFailedPlugins(ctx context.Context, task plugin.Task, budget *llm.Budget,
	sel []dispatch.Selection, outcomes []dispatch.Outcome) ([]dispatch.Outcome, []string) {
	return retryFailedPlugins(w, ctx, task, budget, sel, outcomes)
}
