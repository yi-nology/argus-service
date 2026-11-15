// workflow_finish.go —— 收尾装配：R4+R5 编排壳、证据校验、合并反思与
// 仓库主权过滤。

package requirement

import (
	"context"
	"encoding/json"
	"fmt"
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

	changedFiles := diffPaths(task.Diff)
	skills := skillsForKind(task.Kind)

	sel, outcomes, pruned, retryNotes := w.dispatchR4(ctx, task, budget, rc, changedFiles, skills)

	merged, reflectInfo, err := w.mergeAndReflect(ctx, task, outcomes, pruned, summary, changedFiles)
	if err != nil {
		return nil, err
	}
	w.applyRepoPolicies(merged, rc, task)

	// v6.18.0 Finding 级证据可信度校验（v6.17.0 Claim 级同族、不同面）：在主权
	// 过滤后、综合段与 R5 裁决前——综合段输入与裁决都读到校验后的证据状态；
	// 高危证据不可验证降级 medium 退出一票否决面（与 Claim 高风险降级同纪律）。
	verifyFindingsEvidence(w, merged, task)

	// 批 F 需求追溯链（finish 步骤，与证据校验同层）：issue→AC→代码→测试
	// 四环载荷挂 Artifacts——纯报告增强零裁决影响（不进 R5）。
	if spec != nil {
		if tc := plugin.BuildTraceChain(spec, report, diffPaths(task.Diff)); tc != nil {
			if raw, err := json.Marshal(tc); err == nil {
				task.Artifacts[plugin.ArtifactTraceChain] = string(raw)
			}
		}
	}

	// v6.10.0 分析师综合段：仅 analyst 模板触发，产出失败降级不阻断（analyst.go）。
	analyst := w.synthesisAnalyst(ctx, task, merged, summary)
	// 模板键回声到 plugin.WorkflowOutput（runner 装配时写入 Artifacts）供 buildReport 渲染分发。
	reportTemplate := task.Artifacts[plugin.ArtifactReportTemplate]

	verdict, rule, completeness := w.judgeAndGate(task, spec, report, merged, sel, outcomes, pruned, retryNotes)

	out := &plugin.WorkflowOutput{
		Verdict:        verdict,
		Rule:           rule,
		Spec:           spec,
		Report:         report,
		Merged:         merged,
		PluginsRun:     dispatch.OutcomeNames(outcomes),
		Summary:        summary,
		Elapsed:        time.Since(start),
		Reflect:        reflectInfo,
		Completeness:   completeness,
		Analyst:        analyst,
		ReportTemplate: reportTemplate,
	}
	out.Tokens = w.LLM.UsedTokens()
	return out, nil
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

	// Merger：归一化 + 指纹去重 + 抑制过滤（store 由 Merger 持有）
	merged, err := w.Merger.Merge(ctx, task.PR, task.ID, outcomes, time.Now())
	if err != nil {
		return nil, nil, err
	}
	if summary == "" {
		merged.Degraded = append(merged.Degraded,
			"R2 变更理解失败，变更摘要缺失（R3/R4 直接基于原始 diff 审查，报告不含变更动机摘要）")
	}
	for _, p := range pruned {
		// 预算是被调的，不是天条：把旋钮指给读者（v5.5.3）
		merged.Degraded = append(merged.Degraded,
			p+" 未参与（token 预算裁剪；可调：repo_settings.task_token_budget / ARGUS_TASK_TOKEN_BUDGET）")
	}

	// R3.5 报告反思（v3.4）：合并去重后、分歧标注与 R5 裁决前——被剔除的意见连带
	// 分歧标注一起消失，R5 真值表也基于修订后 findings 求值。指纹库不受影响：
	// 反思是逐轮质量闸门（统计性判断），持久抑制只来自人工 suppress 反馈。
	var reflectInfo *plugin.ReflectSummary
	if w.ReflectRounds > 0 {
		// 活检：开关开着就必须走到 R35；模型未配/无 findings 也算「尝试执行」
		//——否则「配置了反思却从未调用」会与「正常空跑」不可分。
		plugin.ReportLayerExec(plugin.LayerR35)
		w.onStage(plugin.LayerR35)
		reflectInfo = w.reflectFindings(ctx, task, summary, merged)
		if reflectInfo != nil && (reflectInfo.Removed > 0 || reflectInfo.DegradedNoEvidence > 0) {
			note := fmt.Sprintf(
				"R3.5 报告反思：%d 轮（收敛=%v），剔除 %d 条误报嫌疑、修正 %d 条",
				reflectInfo.Rounds, reflectInfo.Converged, reflectInfo.Removed, reflectInfo.Adjusted)
			if reflectInfo.DroppedNoEvidence > 0 {
				note += fmt.Sprintf("；证据门丢弃 %d 条", reflectInfo.DroppedNoEvidence)
			}
			if reflectInfo.DegradedNoEvidence > 0 {
				note += fmt.Sprintf("；证据门降级 %d 条", reflectInfo.DegradedNoEvidence)
			}
			merged.Degraded = append(merged.Degraded, note)
		}
	}

	// 学习环低置信标注重放：R3.5 按 id 回写 comment 会覆盖 Merger 阶段的后缀
	// （docs/review-closed-loop-design.md §3.5）。幂等。必须先于交叉分歧标注——
	// 交叉标注文本先入 Comment 会让重放 matchLowPattern 的键失配（PatternKey 由
	// 评语派生），被交叉标注的条目永远补不上低置信标注（v3.9.1）。
	if w.Merger != nil {
		w.Merger.AnnotateLowConfidence(ctx, task.PR, merged)
	}

	// 交叉审查分歧标注（v3.3）：编队成员对同一区域意见不一致时标给人审
	if members := w.Dispatcher.ActiveSquadMembers(&task.PR, changedFiles); len(members) >= 2 {
		w.Merger.AnnotateCrossReview(merged, members)
	}
	return merged, reflectInfo, nil
}

// applyRepoPolicies 仓库主权过滤（确定性，裁决仍不走 LLM）：review.min_severity
// 低于阈值者不进报告、不参与 R5 裁决；路径排除的覆盖收缩必须可见。
func (w *Workflow) applyRepoPolicies(merged *plugin.MergedFindings, rc *plugin.RepoConfig, task plugin.Task) {
	// 仓库级严重度阈值（v3.15.0 review.min_severity）
	if rc != nil && rc.Review.MinSeverity != "" {
		kept, dropped := plugin.FilterByMinSeverity(merged.Findings, rc.Review.MinSeverity)
		if dropped > 0 {
			merged.Findings = kept
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("仓库配置 review.min_severity=%s：%d 条低于阈值的意见未纳入报告与裁决",
					rc.Review.MinSeverity, dropped))
		}
	}
	// 仓库级路径排除透出（v3.15.0）：被 exclude/include 排除的文件对模型不可见，
	// 覆盖范围收缩必须可见（与截断/增量注记同哲学）。
	if task.Diff != nil {
		excluded := 0
		for _, f := range task.Diff.Files {
			if f.RepoExcluded {
				excluded++
			}
		}
		if excluded > 0 {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("仓库配置 review 路径过滤：%d 个变更文件被排除在审查之外（.reviewagents.yaml）", excluded))
		}
	}
}
