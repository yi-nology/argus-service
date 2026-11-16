// workflow_finish.go —— 收尾装配：R4+R5 编排壳、证据校验、合并反思与
// 仓库主权过滤。

package requirement

import (
	"context"
	"time"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

// finish = R4 专家委派 + R5 确定性真值表。
// finish = R4 专家委派 + R5 确定性裁决的编排壳。v3.55.0 拆分为四个命名子步骤，
// 顺序即语义（委派 → 合并/反思/标注 → 仓库主权过滤 → 裁决与门），步骤间只传
// 显式产物——此前 210 行单体函数是勘察在案的最大可读性债。
func (w *Workflow) finish(ctx context.Context, task plugin.Task, budget *llm.Budget,
	rc *plugin.RepoConfig, start time.Time, spec *plugin.RequirementSpec, report *plugin.ComplianceReport,
	summary string) (*plugin.WorkflowOutput, error) {
	return finish(w, ctx, task, budget, rc, start, spec, report, summary)
}

// verifyFindingsEvidence Finding 级证据校验接线缝（v6.18.0，finish 步骤）：
// 校验面 = 改动集（Diff.Files.Path）+ 触达集（ArtifactCodegraphTouched 渲染面
// 提取的 file:line 引用），与 Claim 级 synthesisAnalyst 内的校验同源不同面。
// 纯委托 plugin.VerifyFindingsEvidence——独立命名是为可测接线（finish 组合由
// golden e2e 覆盖，本缝单测钉住参数装配：changed/touched 两集的取数正确性）。
func verifyFindingsEvidence(w *Workflow, merged *plugin.MergedFindings, task plugin.Task) {
	plugin.VerifyFindingsEvidence(merged, changedPaths(task), touchedPaths(task), w.SourceReader)
}

// mergeAndReflect 合并与修订：Merger 归一化/指纹去重/抑制过滤 → R2 缺失与预算
// 裁剪注明 → R3.5 报告反思 → 学习环低置信标注重放 → 交叉审查分歧标注。
func (w *Workflow) mergeAndReflect(ctx context.Context, task plugin.Task, outcomes []dispatch.Outcome,
	pruned []string, summary string, changedFiles []string) (*plugin.MergedFindings, *plugin.ReflectSummary, error) {
	return mergeAndReflect(w, ctx, task, outcomes, pruned, summary, changedFiles)
}

// applyRepoPolicies 仓库主权过滤（确定性，裁决仍不走 LLM）：review.min_severity
// 低于阈值者不进报告、不参与 R5 裁决；路径排除的覆盖收缩必须可见。
func (w *Workflow) applyRepoPolicies(merged *plugin.MergedFindings, rc *plugin.RepoConfig, task plugin.Task) {
	applyRepoPolicies(w, merged, rc, task)
}
