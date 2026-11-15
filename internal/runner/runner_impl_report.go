// runner_impl_report.go —— Runner 提示、报告与模板裁决的包级实现。

package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
)

// reviewHints 的包级实现（转发壳见原文件）。
func reviewHints(r *Runner, ctx context.Context, pr *plugin.PRContext) string {
	if pr == nil || !r.Learning.Enabled || r.Store == nil {
		return ""
	}
	list, err := r.Store.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
		r.Learning.MinSamples, r.Learning.InjectFloor, r.Learning.InjectTopN)
	if err != nil || len(list) == 0 {
		if err != nil {
			r.Log.Warn("argus.regret.hints_failed", "error", err.Error())
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("本仓库历史上以下审查模式误报率偏高（开发者 suppress / 终审剔除）。")
	b.WriteString("命中类似表述时：给出具体行与影响，否则不要输出。\n")
	for _, p := range list {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		fmt.Fprintf(&b, "- [%s] %s（置信度 %.2f，samples=%d）\n", p.Plugin, sample, p.Confidence, p.Samples)
	}
	out := b.String()
	// 注入留痕（可观测性补齐）：提示词面注入此前零日志——线上排查「为什么这轮
	// 意见变少」时无法区分提示词收敛与模型随机性。
	r.Log.Info("argus.regret.hints_injected", "pr", fmt.Sprintf("%s/%s/%s#%s",
		pr.Platform, pr.Owner, pr.Repo, pr.Number), "patterns", len(list), "bytes", len(out))
	return out
}

// prevFindingsSection 的包级实现（转发壳见原文件）。
func prevFindingsSection(r *Runner, ctx context.Context, pr *plugin.PRContext) string {
	if r.Store == nil {
		return ""
	}
	prints, err := r.Store.ListFindingPrints(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		r.Log.Warn("argus.runner.prev_findings_failed", "error", err.Error())
		return ""
	}
	lines := make([]string, 0, len(prints))
	for _, p := range prints {
		if p.Status != storage.PrintOpen || p.Comment == "" || p.File == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s:%d [%s] %s",
			p.File, p.Line, strings.ToUpper(p.Severity), textutil.TruncEllipsis(p.Comment, 160)))
		if len(lines) >= 15 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// costRate 的包级实现（转发壳见原文件）。
func costRate(r *Runner, mdl string) [2]float64 {
	if r.CostPer1K != nil {
		return r.CostPer1K(mdl)
	}
	return [2]float64{}
}

// buildReport 的包级实现（转发壳见原文件）。
func buildReport(r *Runner, task *storage.Task, pr *plugin.PRContext, out *plugin.WorkflowOutput,
	taskCost *llm.CostTracker) reportview.Report {
	rep := reportview.Report{
		TaskID:  task.ID,
		HeadSHA: pr.HeadSHA,
		Version: r.Version,
		Meta:    r.Cfg.ReportMeta,
		Plugins: out.PluginsRun,
		Elapsed: out.Elapsed,
		Tokens:  out.Tokens,
		FixHint: r.Cfg.FixAgent != "", // v3.85.0：fixer 启用时意见行附一键修复入口
	}
	// v6.10.0 报告契约模板：渲染分发键与综合段产物（finish 从 Artifacts 回声到
	// Output，这里直读；Analyst 为 nil（非 analyst/降级）时渲染层自然退化为
	// standard 骨架）。
	rep.Template = out.ReportTemplate
	rep.Analyst = out.Analyst
	if taskCost != nil {
		var usd float64
		for _, ms := range taskCost.Summary() {
			usd += ms.TotalCostUSD
		}
		rep.CostUSD = usd
	}
	if out.Merged != nil {
		rep.Findings = out.Merged.Findings
		rep.Degraded = out.Merged.Degraded
		rep.Suppressed = out.Merged.Suppressed
		rep.CrossReview = out.Merged.CrossReview
	}
	// G4 完备性段（DegradedNotes nil 安全：未启用/全通过 → nil）
	rep.Completeness = out.Completeness.DegradedNotes()
	if out.Spec != nil {
		rep.ShowCompliance = true
		rep.ReqSource = out.Spec.Source
		// 预建 check map，O(1) 查找替代 O(N*M) 嵌套循环
		checkMap := map[string]*plugin.ACCheck{}
		if out.Report != nil {
			for i := range out.Report.Checks {
				checkMap[out.Report.Checks[i].ID] = &out.Report.Checks[i]
			}
		}
		for _, ac := range out.Spec.ACs {
			view := reportview.ACCheckView{ID: ac.ID, Text: ac.Text, Status: plugin.ACUnverifiable, Evid: "未判定"}
			if c, ok := checkMap[ac.ID]; ok {
				view.Status = c.Status
				view.Evid = textutil.FirstNonEmpty(c.Evidence, c.Detail)
			}
			rep.ACs = append(rep.ACs, view)
		}
		if out.Report != nil {
			for _, sc := range out.Report.ScopeCreep {
				rep.ScopeCreep = append(rep.ScopeCreep, reportview.ScopeView{File: sc.File, Note: sc.Note})
			}
		}
	}
	return rep
}

// resolveReportTemplate 的包级实现（转发壳见原文件）。
func resolveReportTemplate(r *Runner, rc *plugin.RepoConfig) string {
	repo := ""
	if rc != nil {
		repo = rc.Review.ReportTemplate
	}
	return reporttmpl.Resolve(repo, r.Cfg.ReportTemplate)
}

// resolveReportTemplateChain 的包级实现（转发壳见原文件）。
func resolveReportTemplateChain(r *Runner, rc *plugin.RepoConfig, knobs taskKnobs) string {
	if knobs.reportTemplate != "" {
		return knobs.reportTemplate
	}
	return r.resolveReportTemplate(rc)
}
