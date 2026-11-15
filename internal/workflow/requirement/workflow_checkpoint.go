// workflow_checkpoint.go —— 任务级 checkpoint 复用与落库（R1 spec / R2 摘要）。

package requirement

import (
	"context"
	"encoding/json"

	"github.com/yi-nology/argus/internal/plugin"
)

// checkpointSpec 任务级 checkpoint 复用：R1 spec（坏 JSON 视为无 checkpoint）。
func (w *Workflow) checkpointSpec(task plugin.Task) *plugin.RequirementSpec {
	raw := task.Artifacts[plugin.ArtifactCkptR1Spec]
	if raw == "" {
		return nil
	}
	var spec plugin.RequirementSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		w.Log.Warn("argus.checkpoint.r1_corrupt", "error", err.Error(), "effect", "全量重跑 R1")
		return nil
	}
	return &spec
}

// checkpointSummary 任务级 checkpoint 复用：R2 摘要（reused=false = 无 checkpoint）。
func (w *Workflow) checkpointSummary(task plugin.Task) (summary string, reused bool) {
	if s := task.Artifacts[plugin.ArtifactCkptR2Summary]; s != "" {
		return s, true
	}
	return "", false
}

// checkpointSave 阶段产物落库（CheckpointSink nil = 不落；失败由 sink 实现方告警，
// 不影响主流程——checkpoint 缺失只影响重试成本）。
func (w *Workflow) checkpointSave(patch map[string]string) {
	if w.CheckpointSink == nil {
		return
	}
	w.CheckpointSink(patch)
}

// summaryWithCheckpoint R2 摘要统一入口（Run 与 RunReviewOnly 共用）：checkpoint
// 命中直接复用不调 LLM；未命中调 SummarizeChange，成功即 checkpoint 落库，失败
// 降级空摘要继续管线。v3.49.0 起 code_review/visual_check 任务重试同样免 R2
// 重复跑（此前 checkpoint 只覆盖 requirement 路径）。
func (w *Workflow) summaryWithCheckpoint(ctx context.Context, task plugin.Task) (summary string, reused bool) {
	if s, ok := w.checkpointSummary(task); ok {
		return s, true
	}
	summary, err := w.SummarizeChange(ctx, task)
	if err != nil {
		w.Log.Warn("argus.r2.degrade", "error", err.Error())
		return "", false
	}
	w.checkpointSave(map[string]string{plugin.ArtifactCkptR2Summary: summary})
	return summary, false
}
