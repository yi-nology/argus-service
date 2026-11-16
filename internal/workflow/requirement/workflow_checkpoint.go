// workflow_checkpoint.go —— 任务级 checkpoint 复用与落库（R1 spec / R2 摘要）。

package requirement

import (
	"context"

	"github.com/yi-nology/argus/internal/plugin"
)

// checkpointSpec 任务级 checkpoint 复用：R1 spec（坏 JSON 视为无 checkpoint）。
func (w *Workflow) checkpointSpec(task plugin.Task) *plugin.RequirementSpec {
	return checkpointSpec(w, task)
}

// checkpointSummary 任务级 checkpoint 复用：R2 摘要（reused=false = 无 checkpoint）。
func (w *Workflow) checkpointSummary(task plugin.Task) (summary string, reused bool) {
	return checkpointSummary(w, task)
}

// checkpointSave 阶段产物落库（CheckpointSink nil = 不落；失败由 sink 实现方告警，
// 不影响主流程——checkpoint 缺失只影响重试成本）。
func (w *Workflow) checkpointSave(patch map[string]string) {
	checkpointSave(w, patch)
}

// summaryWithCheckpoint R2 摘要统一入口（Run 与 RunReviewOnly 共用）：checkpoint
// 命中直接复用不调 LLM；未命中调 SummarizeChange，成功即 checkpoint 落库，失败
// 降级空摘要继续管线。v3.49.0 起 code_review/visual_check 任务重试同样免 R2
// 重复跑（此前 checkpoint 只覆盖 requirement 路径）。
func (w *Workflow) summaryWithCheckpoint(ctx context.Context, task plugin.Task) (summary string, reused bool) {
	return summaryWithCheckpoint(w, ctx, task)
}
