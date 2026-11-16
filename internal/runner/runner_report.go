// runner_report.go —— 报告产物面：buildReport/assembleReport/ResultPayload/成本辅助
// （公告面见 runner_announce.go，审查提示注入见 runner_hints.go）。

package runner

import (
	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/go-git-platform/model"
)

// costRate 模型单价（v3.36.0 起统一按配置费率近似，未知模型不再按 0 计）。
func (r *Runner) costRate(mdl string) [2]float64 {
	return costRate(r, mdl)
}

// cacheAdjustedInput 按缓存命中折扣摊薄输入单价（v3.180.0 成本口径修正）。
// cached ⊆ prompt 是命中前缀缓存的 prompt token（便宜得多，cacheRatio=0.1）；
// 旧记账把整块 prompt 按全价入账 → 成本高估。此处把 InputPerM 摊薄为
// (uncached + cached×ratio)/prompt，**token 计数保持真实**（PromptTokens 不被
// 「等效计费量」污染）。cached<=0 或 prompt<=0 时原价返回；cached 超 prompt 钳回。
func cacheAdjustedInput(inputPerM float64, prompt, cached int, cacheRatio float64) float64 {
	if cached <= 0 || prompt <= 0 {
		return inputPerM
	}
	if cached > prompt {
		cached = prompt
	}
	blended := (float64(prompt-cached) + float64(cached)*cacheRatio) / float64(prompt)
	return inputPerM * blended
}

// buildReport 报告输入组装（模板渲染交给 reportview）。taskCost 非 nil 时附成本摘要。
func (r *Runner) buildReport(task *storage.Task, pr *plugin.PRContext, out *plugin.WorkflowOutput,
	taskCost *llm.CostTracker) reportview.Report {
	return buildReport(r, task, pr, out, taskCost)
}

// ResultPayload GetTask 返回的结构化结果（result_json 列）。
type ResultPayload struct {
	Verdict    string                   `json:"verdict"`
	Rule       string                   `json:"rule"`
	Findings   []domain.Finding         `json:"findings"`
	Report     *plugin.ComplianceReport `json:"compliance_report,omitempty"`
	ScopeCreep []plugin.ScopeCreepItem  `json:"scope_creep,omitempty"`
	Summary    string                   `json:"change_summary,omitempty"`
	Plugins    []string                 `json:"plugins"`
	Degraded   []string                 `json:"degraded,omitempty"`
	// Completeness 完备性裁判结构化产出（v3.13.0 G7 数据出口；nil = 未启用/全通过）。
	// GET /tasks/:id 与 MCP get_report 的消费方据此渲染「审得全不全」。
	Completeness *plugin.CompletenessReport `json:"completeness,omitempty"`
	// DiffText 本次审查的 diff 原文（增量审查时用于 diff-of-diffs 比对）。
	DiffText string `json:"diff_text,omitempty"`
	// Analyst 分析师综合段产物（v6.10.0；nil = 非 analyst/综合段失败降级）。
	// 结构化消费面（报告页/IDE）据此渲染声明对照/结构维度，无需解析 markdown。
	Analyst *plugin.AnalystSections `json:"analyst,omitempty"`
	// ReportTemplate 本任务裁决后的报告模板键（v6.10.0）。
	ReportTemplate string `json:"report_template,omitempty"`
}

func toResultPayload(out *plugin.WorkflowOutput, diffText string) *ResultPayload {
	p := &ResultPayload{Verdict: out.Verdict, Rule: out.Rule, Plugins: out.PluginsRun, Summary: out.Summary, DiffText: diffText}
	if out.Merged != nil {
		p.Findings = out.Merged.Findings
		p.Degraded = out.Merged.Degraded
	}
	p.Report = out.Report
	if out.Report != nil {
		p.ScopeCreep = out.Report.ScopeCreep
	}
	// G7：完备性报告持久化（nil 安全）——结构化产出进 result_json，任何任务
	// 详情消费方（报告页/副驾/IDE 插件）无需解析 markdown 即可拿到
	p.Completeness = out.Completeness
	p.Analyst = out.Analyst
	p.ReportTemplate = out.ReportTemplate
	return p
}

func authorName(u *model.CRUser) string {
	if u == nil {
		return ""
	}
	if u.Username != "" {
		return u.Username
	}
	return u.Name
}

func countMissingPartial(report *plugin.ComplianceReport) int {
	if report == nil {
		return 0
	}
	n := 0
	for _, c := range report.Checks {
		if c.Status == plugin.ACMissing || c.Status == plugin.ACPartial {
			n++
		}
	}
	return n
}

func findingsCount(m *plugin.MergedFindings) int {
	if m == nil {
		return 0
	}
	return len(m.Findings)
}

// ruleOf 审计字段用：终态命中的 R5 规则（out 为 nil 时空串）。
func ruleOf(out *plugin.WorkflowOutput) string {
	if out == nil {
		return ""
	}
	return out.Rule
}
