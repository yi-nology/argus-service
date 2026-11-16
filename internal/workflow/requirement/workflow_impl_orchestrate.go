// workflow_impl_orchestrate.go —— Workflow 顶层编排入口的包级实现。

package requirement

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oagentkit/llm"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// Run 的包级实现（转发壳见原文件）。
func run(w *Workflow, ctx context.Context, task plugin.Task, budget *llm.Budget) (*plugin.WorkflowOutput, error) {
	start := time.Now()
	task.Budget = budget // R3 重试的预算门读取（深挖修复：R1-R3 此前完全不受预算约束）
	// LLM 降级链基线（v6.9.0 P2-04，#15）：run 起点的主备切换计数——judgeAndGate
	// 据差值判定「本轮审查经历了 LLM 降级」并进裁决（R5.L）。
	w.llmFailoverBase = obs.LLMFailoverTotal()
	rc := w.loadRepoConfig(ctx, task)

	debug := map[string]string{}
	var err error
	// R1 需求解析（v3.12.0 G2：同任务重试优先复用已持久化阶段产物——checkpoint
	// 只在任务内有效，新任务 task.Artifacts 无 checkpoint 键，恒走全量解析）
	spec := w.checkpointSpec(task)
	if spec != nil {
		debug["r1_checkpoint"] = "reused"
	} else {
		spec, err = w.ParseRequirement(ctx, task)
		if err != nil {
			return nil, err
		}
		if b, e := json.Marshal(spec); e == nil {
			w.checkpointSave(map[string]string{plugin.ArtifactCkptR1Spec: string(b)})
		}
	}
	if b, e := json.Marshal(spec); e == nil {
		debug[plugin.DebugKeyR1Spec] = string(b)
	}
	// R2 变更理解。失败降级继续（深挖修复）：R2 只产摘要且 R3/R4 携带原始 diff
	// 不依赖它——此前是全链路唯一"一个摘要模型瞬断就 error 掉整个任务"的单点，
	// 与 R1/R3/R4 的失败降级纪律不一致。
	summary, reused := w.summaryWithCheckpoint(ctx, task)
	if reused {
		debug[plugin.DebugKeyR2Checkpoint] = "reused"
	}
	debug[plugin.DebugKeyR2Summary] = summary
	// 注入 R2 产物到 Artifacts，R4 插件可复用（公共前缀省 token）
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts[plugin.ArtifactChangeSummary] = summary
	task.Requirement = spec

	// R3 完成度判定
	report, raw, err := w.CheckCompliance(ctx, task, spec, summary)
	if err != nil {
		return nil, err
	}
	if raw != "" {
		debug[plugin.DebugKeyR3Raw] = raw
	}
	if report != nil {
		if b, e := json.Marshal(report); e == nil {
			debug[plugin.DebugKeyR3Checks] = string(b)
		}
	}

	// R4 专家委派 + R5 汇总裁决
	out, err := w.finish(ctx, task, budget, rc, start, spec, report, summary)
	if out != nil {
		out.Debug = debug
	}
	return out, err
}

// RunReviewOnly 的包级实现（转发壳见原文件）。
func runReviewOnly(w *Workflow, ctx context.Context, task plugin.Task, budget *llm.Budget) (*plugin.WorkflowOutput, error) {
	start := time.Now()
	task.Budget = budget
	rc := w.loadRepoConfig(ctx, task)
	// R2 摘要供 R4 插件复用（失败降级，同 Run）；checkpoint 复用/落库与 Run 同款
	//（v3.49.0 补齐：此前 code_review/visual_check 任务重试时 R2 每次全量重跑）。
	summary, reused := w.summaryWithCheckpoint(ctx, task)
	if task.Artifacts == nil {
		task.Artifacts = map[string]string{}
	}
	task.Artifacts[plugin.ArtifactChangeSummary] = summary
	out, err := w.finish(ctx, task, budget, rc, start, nil, nil, summary)
	if out != nil {
		out.Debug = map[string]string{plugin.DebugKeyR2Summary: summary}
		if reused {
			out.Debug[plugin.DebugKeyR2Checkpoint] = "reused"
		}
	}
	return out, err
}
