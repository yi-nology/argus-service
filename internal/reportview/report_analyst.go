// report_analyst.go —— analyst/minimal 模板段落：结论/声明表/风险链/坏味/结构维度/缺口。

package reportview

import (
	"fmt"
	"strings"

	"github.com/oagentkit/guardx/fence"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

// renderAnalyst 分析师深度报告（spec §4.6，例文骨架）。段落 N=0 一律省略；
// Analyst=nil（综合段降级）时退化为 standard 骨架（claims/design 等段缺席）。
func renderAnalyst(r Report) string {
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 🔬 Argus 深度审查报告")

	// 一段话总结：结论 + 变更规模（复用 VerdictText，R5 裁决口径不动）。
	fmt.Fprintf(&b, "\n\n**结论：%s。**", r.VerdictText)
	if scale := analystScaleLine(r); scale != "" {
		fmt.Fprintf(&b, " %s。", scale)
	}
	b.WriteString("\n")

	if r.Analyst != nil {
		renderClaimsTable(&b, r.Analyst.Claims)
	}

	// 风险链（high/critical）与代码坏味（medium/low）分组渲染，组内保持
	// severity 降序（merger 已排序，分组即切片）。
	risks, smells := splitFindings(r.Findings)
	renderRiskChain(&b, r, risks)
	renderSmells(&b, r, smells)

	if r.Analyst != nil {
		renderDesignSection(&b, r.Analyst.Design)
		renderGapsAndQuestions(&b, r.Analyst.TestGaps, r.Analyst.OpenQuestions)
	}

	// issue 关联场景的确定性验收清单与 scope creep 保留（与 claims 表并存：
	// 清单是确定性判定、claims 是模型对 MR 描述的取证）。
	if r.ShowCompliance {
		renderCompliance(&b, r)
	}
	if r.ImpactSection != "" {
		b.WriteString(r.ImpactSection)
	}
	if r.TraceSection != "" {
		b.WriteString(r.TraceSection)
	}
	renderDegradedAndCompleteness(&b, r)

	b.WriteString("\n### 结论: ")
	b.WriteString(r.VerdictText)
	if r.Rule != "" {
		fmt.Fprintf(&b, "（%s）", r.Rule)
	}
	b.WriteString("\n")

	if r.Meta {
		renderMeta(&b, r)
	}
	return b.String()
}

// renderMinimal 极简模板：结论先行 + 仅 high/critical findings（封顶
// minimalMaxFindings，超出注计数）+ 一行 meta。给"别吵我"的仓。
func renderMinimal(r Report) string {
	const minimalMaxFindings = 10
	var b strings.Builder
	b.WriteString(ReportHeaderHTML(r.TaskID, r.HeadSHA, r.Version))
	b.WriteString("\n## 📋 Argus 审查结论\n\n**")
	b.WriteString(r.VerdictText)
	b.WriteString("**\n")

	var highs []domain.Finding
	for _, f := range r.Findings {
		if f.Severity == "high" || f.Severity == "critical" {
			highs = append(highs, f)
		}
	}
	if len(highs) > 0 {
		b.WriteString("\n### 高危意见\n")
		n := min(len(highs), minimalMaxFindings)
		for _, f := range highs[:n] {
			comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
			fmt.Fprintf(&b, "- `%s:%d` **[%s]** %s\n",
				fence.EscapeUntrusted(f.File), f.Line, strings.ToUpper(f.Severity), comment)
		}
		if len(highs) > minimalMaxFindings {
			fmt.Fprintf(&b, "\n> 另有 %d 条高危/严重意见从略（极简模板封顶 %d 条，详见行内评论）。\n",
				len(highs)-minimalMaxFindings, minimalMaxFindings)
		}
	}
	if r.Meta {
		fmt.Fprintf(&b, "\n> task `%s` · 耗时 %ds · tokens %d", r.TaskID, int(r.Elapsed.Seconds()), r.Tokens)
		if ci := ciStatusLabel(r.CIStatus); ci != "" {
			fmt.Fprintf(&b, " · %s", ci)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// analystScaleLine 规模统计（例文「7 文件 +519/−579」形态；v6.11.0 接入真实
// diff 统计——v6.10.0 留空实现已兑现）。无 diff 数据（ScaleFiles=0）整行省略。
func analystScaleLine(r Report) string {
	if r.ScaleFiles == 0 {
		return ""
	}
	line := fmt.Sprintf("规模：%d 文件", r.ScaleFiles)
	if r.ScaleAdditions > 0 || r.ScaleDeletions > 0 {
		line += fmt.Sprintf("（+%d/−%d）", r.ScaleAdditions, r.ScaleDeletions)
	}
	return line
}

// ciStatusLabel 统一 CI 状态 → 展示短标签（图标 + 中文；空输入返回空串，调用方省略）。
func ciStatusLabel(s string) string {
	switch s {
	case "success":
		return "✅ CI 通过"
	case "failure", "error":
		return "❌ CI 失败"
	case "canceled":
		return "⏹ CI 已取消"
	case "pending", "running":
		return "⏳ CI 进行中"
	default:
		return ""
	}
}

// claimIcon 声明判定五态图标（词表 plugin.ClaimStatus，spec §4.2）。
func claimIcon(status string) string {
	switch status {
	case plugin.ClaimImplemented:
		return "✅"
	case plugin.ClaimPartial:
		return "⚠️"
	case plugin.ClaimMissing:
		return "❌"
	case plugin.ClaimUndeclared:
		return "➕"
	case plugin.ClaimInconsistent:
		return "❗"
	default:
		return "❓"
	}
}

// claimStatusLabel 判定中文标签（表格「判定」列）。
func claimStatusLabel(status string) string {
	switch status {
	case plugin.ClaimImplemented:
		return "实现"
	case plugin.ClaimPartial:
		return "部分"
	case plugin.ClaimMissing:
		return "未实现"
	case plugin.ClaimUndeclared:
		return "超出声明"
	case plugin.ClaimInconsistent:
		return "口径不一致"
	default:
		return status
	}
}

// renderClaimsTable 声明对照表（| # | 声明项 | 代码证据 | 判定 |，例文一节形态）。
// 空表整段省略。
func renderClaimsTable(b *strings.Builder, claims []plugin.Claim) {
	if len(claims) == 0 {
		return
	}
	b.WriteString("\n\n### 一、需求符合性（声明 vs 代码）\n\n")
	b.WriteString("| # | 声明项 | 代码证据 | 判定 |\n|---|---|---|---|\n")
	for i, c := range claims {
		claim := strings.ReplaceAll(fence.EscapeUntrusted(c.Claim), "\n", " ")
		evid := strings.ReplaceAll(fence.EscapeUntrusted(c.Evidence), "\n", " ")
		if evid == "" {
			evid = "—"
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s %s |\n",
			i+1, claim, evid, claimIcon(c.Status), claimStatusLabel(c.Status))
	}
}

// splitFindings 按严重度切风险链/坏味两组（high/critical = 风险链主体；
// 其余 = 坏味组，编号渲染）。merger 已降序，切片保持序。
func splitFindings(findings []domain.Finding) (risks, smells []domain.Finding) {
	for _, f := range findings {
		if f.Severity == "high" || f.Severity == "critical" {
			risks = append(risks, f)
		} else {
			smells = append(smells, f)
		}
	}
	return risks, smells
}

// renderRiskChain 风险链（🔴🟠 分级，chain 证据链缩进子段 + 建议修复块）。
func renderRiskChain(b *strings.Builder, r Report, risks []domain.Finding) {
	if len(risks) == 0 {
		return
	}
	b.WriteString("\n### 二、风险\n")
	for i, f := range risks {
		by := f.By
		if by == "" {
			by = "argus"
		}
		icon := "🟠"
		if f.Severity == "critical" {
			icon = "🔴"
		}
		comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
		fmt.Fprintf(b, "\n#### %s R%d（%s）`%s:%d`\n\n%s — by %s\n",
			icon, i+1, strings.ToUpper(f.Severity),
			fence.EscapeUntrusted(f.File), f.Line, comment, fence.EscapeUntrusted(by))
		b.WriteString(evidenceLine(f, "> "))
		if chain := strings.TrimSpace(fence.EscapeUntrusted(f.Chain)); chain != "" {
			chain = strings.ReplaceAll(chain, "\n", "\n> ")
			fmt.Fprintf(b, "\n> **证据链**：%s\n", chain)
		}
		if f.SuggestedFix != "" {
			n := codeFenceLen(f.SuggestedFix)
			fenceStr := strings.Repeat("`", n)
			fix := strings.ReplaceAll(f.SuggestedFix, "\n", "\n  ")
			fmt.Fprintf(b, "\n**建议修复：**\n\n%s\n%s\n%s\n", fenceStr, fix, fenceStr)
		}
	}
}

// renderSmells 代码坏味（🟡🟢 组，编号渲染，例文「三、代码坏味」形态）。
func renderSmells(b *strings.Builder, r Report, smells []domain.Finding) {
	if len(smells) == 0 {
		return
	}
	b.WriteString("\n### 三、代码坏味\n")
	for i, f := range smells {
		comment := strings.ReplaceAll(strings.TrimSpace(fence.EscapeUntrusted(f.Comment)), "\n", "\n  ")
		fmt.Fprintf(b, "\n%d. **`%s:%d`** — %s\n",
			i+1, fence.EscapeUntrusted(f.File), f.Line, comment)
		b.WriteString(evidenceLine(f, "   "))
		if f.SuggestedFix != "" {
			n := codeFenceLen(f.SuggestedFix)
			fenceStr := strings.Repeat("`", n)
			fix := strings.ReplaceAll(f.SuggestedFix, "\n", "\n   ")
			fmt.Fprintf(b, "   %s\n   %s\n   %s\n", fenceStr, fix, fenceStr)
		}
	}
}

// renderDesignSection 结构维度（design.Triggered 才渲染；对象图/内聚/耦合/迁移）。
func renderDesignSection(b *strings.Builder, d *plugin.Design) {
	if d == nil || !d.Triggered {
		return
	}
	b.WriteString("\n### 四、结构维度（对象生命周期 × 内聚/耦合）\n")
	writeBulletGroup := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(b, "\n**%s**\n", title)
		for _, it := range items {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(it))
		}
	}
	writeBulletGroup("对象图（创建者→持有者→写者→读者→生命周期）", d.ObjectGraph)
	writeBulletGroup("内聚", d.Cohesion)
	writeBulletGroup("耦合", d.Coupling)
	if m := strings.TrimSpace(d.Migration); m != "" {
		fmt.Fprintf(b, "\n**迁移建议**\n\n%s\n", fence.EscapeUntrusted(m))
	}
}

// renderGapsAndQuestions 测试缺口与待确认清单（例文「四、测试与待确认」形态）。
func renderGapsAndQuestions(b *strings.Builder, gaps, questions []string) {
	if len(gaps) == 0 && len(questions) == 0 {
		return
	}
	if len(gaps) > 0 {
		b.WriteString("\n### 五、测试缺口\n")
		for _, g := range gaps {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(g))
		}
	}
	if len(questions) > 0 {
		b.WriteString("\n### 六、待确认\n")
		for _, q := range questions {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(q))
		}
	}
}

// renderDegradedAndCompleteness 降级说明 + 完备性段（analyst/standard 共用尾部）。
func renderDegradedAndCompleteness(b *strings.Builder, r Report) {
	if len(r.Degraded) > 0 {
		b.WriteString("\n<details><summary>插件降级说明</summary>\n\n")
		for _, d := range r.Degraded {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(d))
		}
		b.WriteString("\n</details>\n")
	}
	if len(r.Completeness) > 0 {
		b.WriteString("\n### ⚠️ 完备性检查（覆盖可能不完整）\n")
		for _, c := range r.Completeness {
			fmt.Fprintf(b, "- %s\n", fence.EscapeUntrusted(c))
		}
	}
}
