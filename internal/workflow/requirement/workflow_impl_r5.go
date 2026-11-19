// workflow_impl_r5.go —— Workflow.R5 收尾/裁决的包级实现。

package requirement

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/llm"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// finish 的包级实现（转发壳见原文件）。
func finish(w *Workflow, ctx context.Context, task plugin.Task, budget *llm.Budget,
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

	verdict, rule, completeness := w.judgeAndGate(ctx, task, spec, report, merged, sel, outcomes, pruned, retryNotes)

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

// mergeAndReflect 的包级实现（转发壳见原文件）。
func mergeAndReflect(w *Workflow, ctx context.Context, task plugin.Task, outcomes []dispatch.Outcome,
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
		plugin.ReportLayerExec(ctx, plugin.LayerR35)
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
	// （docs/design/review-closed-loop-design.md §3.5）。幂等。必须先于交叉分歧标注——
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

// applyRepoPolicies 的包级实现（转发壳见原文件）。
func applyRepoPolicies(w *Workflow, merged *plugin.MergedFindings, rc *plugin.RepoConfig, task plugin.Task) {
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

// loadRepoConfig 的包级实现（转发壳见原文件）。
func loadRepoConfig(w *Workflow, ctx context.Context, task plugin.Task) *plugin.RepoConfig {
	if w.RepoConfig != nil {
		return w.RepoConfig
	}
	pr := task.PR
	branch, err := w.SDK.ResolveBranch(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_fetch_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	pr.DefaultBranch = branch
	rc, err := w.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, branch)
	if err != nil {
		w.Log.Warn("argus.workflow.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := dispatch.ResolveRepoConfig(rc, w.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			w.Log.Warn("argus.workflow.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

// judgeAndGate 的包级实现（转发壳见原文件）。
func judgeAndGate(ctx context.Context, w *Workflow, task plugin.Task, spec *plugin.RequirementSpec,
	report *plugin.ComplianceReport, merged *plugin.MergedFindings, sel []dispatch.Selection,
	outcomes []dispatch.Outcome, pruned []string, retryNotes []string) (verdict, rule string, completeness *plugin.CompletenessReport) {
	w.onStage(plugin.LayerR5)

	// G4 完备性裁判（v3.11.0）：确定性检查「审得全不全」（插件缺失/截断/AC 漏判/
	// 降级审查）。失败项经 plugin.WorkflowOutput.Completeness 进报告独立「完备性检查」段
	//（不并入 Merged.Degraded，避免与插件降级说明重复）；demote 策略在 R5 后降级。
	completeness = JudgeCompleteness(sel, outcomes, pruned, task, spec, report)
	merged.Degraded = append(merged.Degraded, retryNotes...)

	// R5：确定性真值表（按序求值，首条命中即短路；模型自评 verdict 不参与）。
	// v3.8.0：有 diff 文件集时启用 scope 闸门——reviewer 经 get_file 看到未变更
	// 文件后对本次变更之外的文件报 high，此前可直接驱动 R5.2 REQUEST_CHANGES
	//（一票否决了与本次变更无关的意见）；现在越界 high 只计入 COMMENT 级并注明。
	r5Start := time.Now()
	scope := reviewScope(task)
	verdict, rule, outOfScope := VerdictScoped(spec, report, merged.Findings, scope)
	if outOfScope > 0 {
		merged.Degraded = append(merged.Degraded,
			fmt.Sprintf("%d 条意见位于本次变更范围之外（get_file 读到的未变更文件），未计入高危裁决", outOfScope))
	}
	plugin.ReportStageDuration(ctx, plugin.LayerR5, time.Since(r5Start))
	plugin.ReportLayerExec(ctx, plugin.LayerR5)

	// R5.0 守门 + SAST 失败注明（详见各自 helper 注释）。
	verdict, rule = w.r5EmptyOutputGate(outcomes, merged, verdict, rule)
	w.r5SastNotes(merged)
	// G4 降级门（v6.3.0 起缺省 demote）：完备性检查不过时不允许白给 APPROVE。
	// 这是 R5.0 精神向「部分覆盖」的延伸：R5.0 管全缺，R5.C 管部分缺失/截断/漏判
	//（插件失败率/证据充分性/审查降级经完备性检查项进门禁——P2-04 #15 输入面）。
	if !completeness.OK && w.CompletenessPolicy == plugin.CompletenessDemote && verdict == VerdictApprove {
		verdict, rule = VerdictComment, "R5.C"
	}
	// R5.L 门禁（v6.9.0 P2-04，#15）：LLM 主备降级链触发史进裁决——本轮审查期间
	// 发生主备切换 = 部分阶段产物来自降级模型（跨模型质量不可比），APPROVE 不可靠
	// → demote 为 COMMENT + Degraded 注明切换次数。跨任务并发下差值是上界近似
	//（他任务的切换也计入）：误伤方向是「宁可 COMMENT 不放不可靠 APPROVE」。
	if delta := obs.LLMFailoverTotal() - w.llmFailoverBase; delta > 0 {
		merged.Degraded = append(merged.Degraded,
			fmt.Sprintf("LLM 主备切换 %d 次（部分阶段产物来自降级模型），结论可信度受限", delta))
		if verdict == VerdictApprove {
			verdict, rule = VerdictComment, "R5.L"
		}
	}
	// 批 G 视觉审查首期 comment-only（spec §2.3 误报控制）：视觉判断置信度低，
	// 不进 request_changes 否决面（人工终审）——任务类型级保守，与 evidence
	// 降级（意见级）不同层。
	if task.Kind == domain.TaskKindVisualCheck && verdict == VerdictRequestChanges {
		verdict, rule = VerdictComment, "R5.G"
		merged.Degraded = append(merged.Degraded,
			"visual_check 首期 comment-only：视觉判断不进否决面，结论供人工终审")
	}
	plugin.ReportVerdict(ctx, task.PR.Platform, task.Kind, verdict, rule)
	// 增量审查范围透出（v3.8.0）。
	r5IncrementalNotes(task, merged)
	// 大 PR 截断透出（深挖 P0 → v3.11.0 由完备性裁判 CheckDiffTruncated 承接，
	// 注记统一从 completeness.DegradedNotes() 产出，此处不再重复）。
	return verdict, rule, completeness
}

// r5EmptyOutputGate 的包级实现（转发壳见原文件）。
func r5EmptyOutputGate(w *Workflow, outcomes []dispatch.Outcome,
	merged *plugin.MergedFindings, verdict, rule string) (string, string) {
	sastNames := make(map[string]bool, len(w.SASTOutcomes))
	for _, o := range w.SASTOutcomes {
		sastNames[o.Name] = true
	}
	ran := 0
	for _, o := range outcomes {
		if sastNames[o.Name] {
			continue
		}
		if o.SkippedReason == "" && o.Err == nil {
			ran++
		}
	}
	if ran == 0 {
		merged.Degraded = append(merged.Degraded,
			"无任何审查插件实际产出（全被禁用/熔断/跳过，原因见上方 Degraded）——本结论在未审查状态下给出，仅作参考")
		if verdict == VerdictApprove {
			verdict, rule = VerdictComment, "R5.0"
		}
	}
	return verdict, rule
}

// r5SastNotes 的包级实现（转发壳见原文件）。
func r5SastNotes(w *Workflow, merged *plugin.MergedFindings) {
	for _, o := range w.SASTOutcomes {
		if o.Err != nil {
			merged.Degraded = append(merged.Degraded,
				fmt.Sprintf("SAST %s 扫描失败：%s（审查继续）", o.Name, logredact.Redact(o.Err.Error())))
		}
	}
}
