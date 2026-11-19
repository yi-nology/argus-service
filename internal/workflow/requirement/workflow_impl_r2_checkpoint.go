// workflow_impl_r2_checkpoint.go —— Workflow.R2 变更理解与阶段检查点的包级实现。

package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// SummarizeChange 的包级实现（转发壳见原文件）。
func summarizeChange(w *Workflow, ctx context.Context, task plugin.Task) (string, error) {
	start := time.Now()
	defer func() {
		plugin.ReportStageDuration(ctx, plugin.LayerR2, time.Since(start))
		plugin.ReportLayerExec(ctx, plugin.LayerR2)
	}()
	w.onStage(plugin.LayerR2)

	if task.Diff == nil || strings.TrimSpace(task.Diff.Text) == "" {
		return "（无有效 diff 内容）", nil
	}
	msgs := []*schema.Message{
		schema.SystemMessage(r2System),
		schema.UserMessage(DataFence(ctx, "git diff（已清洗）", task.Diff.Text)),
	}
	resp, err := w.LLM.Generate(ctx, plugin.LayerR2, msgs)
	if err != nil {
		return "", err
	}
	summary := truncSummary(resp.Content, 1200)
	if rc := utf8.RuneCountInString(strings.TrimSpace(summary)); rc < r2MinValidRunes {
		// 软失败路径（v3.42.0）：R2 是自由文本输出、无结构化失败信号——这是
		// 「R2 失效无失败路径可钉」（v3.26.0 在案）的根因。最小有效性校验
		//（空/过短）给路径钉上失败语义：带反馈重试一次，仍无效则上抛错误走
		// 调用方既有降级（summary=""，R3/R4 携带原始 diff 不受影响；不写
		// checkpoint，任务重试时 R2 可重来）。
		obs.IncrR2SoftFailure(ctx)
		w.Log.Warn("argus.r2.soft_failure", "runes", rc, "action", "带反馈重试一次")
		retryMsgs := append(msgs, schema.UserMessage(plugin.ReminderNote("retry-feedback",
			"上一轮变更摘要输出为空或过短，无法用作变更理解。请基于 diff 输出一段完整摘要：概述改动了哪些文件、每个文件改了什么（不少于两句话）。")))
		if resp2, e2 := w.LLM.Generate(ctx, plugin.LayerR2, retryMsgs); e2 == nil {
			if s2 := truncSummary(resp2.Content, 1200); utf8.RuneCountInString(strings.TrimSpace(s2)) >= r2MinValidRunes {
				return s2, nil
			}
		}
		return "", fmt.Errorf("R2 变更摘要两次输出均无效（空/过短）")
	}
	return summary, nil
}

// checkpointSpec 的包级实现（转发壳见原文件）。
func checkpointSpec(w *Workflow, task plugin.Task) *plugin.RequirementSpec {
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

// checkpointSummary 的包级实现（转发壳见原文件）。
func checkpointSummary(w *Workflow, task plugin.Task) (summary string, reused bool) {
	if s := task.Artifacts[plugin.ArtifactCkptR2Summary]; s != "" {
		return s, true
	}
	return "", false
}

// checkpointSave 的包级实现（转发壳见原文件）。
func checkpointSave(w *Workflow, patch map[string]string) {
	if w.CheckpointSink == nil {
		return
	}
	w.CheckpointSink(patch)
}

// summaryWithCheckpoint 的包级实现（转发壳见原文件）。
func summaryWithCheckpoint(w *Workflow, ctx context.Context, task plugin.Task) (summary string, reused bool) {
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
