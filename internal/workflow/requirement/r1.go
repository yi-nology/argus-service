// r1.go —— R1 需求解析面：PR 描述/issue 分段摘要 + guardrails（AC 上限/截断/歧义收敛）。

package requirement

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/yi-nology/argus/internal/plugin"
)

// issueRefRe 从分支名/PR 描述中提取关联 issue（需求源三入口之一）。
var issueRefRe = regexp.MustCompile(`#(\d{1,8})`)

// maxIssueRefs 关联 issue 数量上限（防恶意刷引用）。
const maxIssueRefs = 5

// maxACs R1 产出的验收条目上限：超限条目转 Ambiguous（R5.3 人工确认），
// 防超长需求改写出上百条 AC 拖垮 R3、也让"清单过大"显式落人工拆分。
const maxACs = 30

// maxACTextRunes 单条 AC 文本上限（字符），防 LLM 把整段原文塞进单条 AC。
const maxACTextRunes = 500

// maxDigestChunks 两级提炼时单段最多提炼的分块数（每块 bodyMax 字符）——
// 超出部分截断留痕，防恶意超长文本制造海量 LLM 调用。
const maxDigestChunks = 8

// reqSegment R1 需求源分段（PR 描述 / 各关联 issue）。
type reqSegment struct {
	label     string
	content   string
	rawChars  int
	truncated bool
}

// ParseRequirement R1：需求解析 → RequirementSpec。
// 需求源：PR 描述 / 关联 issue / 分支名 #N。LLM 失败按"需求源全缺"降级（§10.2）。
// 长度策略：单段硬上限（bodyMax×8）→ 总量 ≤ bodyMax 原文直进单次解析；
// 超过则两级提炼（R1a 按 bodyMax 分块逐块提炼要点 → 合并后进 R1 解析）。
// 任何截断/提炼路径都留痕 Ambiguous → R5.3 人工确认，绝不静默丢内容。
func (w *Workflow) ParseRequirement(ctx context.Context, task plugin.Task) (*plugin.RequirementSpec, error) {
	return parseRequirement(w, ctx, task)
}

// collectReqSegments 需求段收集：PR 描述段（digestMax 硬上限截断）+ 关联 issue
// 正文段（分支名 + 描述中 #N 去重封顶，issueMax×8 截断）。截断注记随段收集留痕。
func (w *Workflow) collectReqSegments(ctx context.Context, pr plugin.PRContext,
	digestMax, issueMax int) (segments []reqSegment, refs []string, truncNotes []string) {
	return collectReqSegments(w, ctx, pr, digestMax, issueMax)
}

// applyR1Guardrails 解析后卫生防线（全部留痕 → Ambiguous → R5.3 人工确认）：
// 来源缺省、sanitize 告警、issue 引用回填、截断/两级提炼留痕、空清单兜底、
// AC 条数上限（超限转 Ambiguous 不丢弃）、单条 AC 文本截断。
func (w *Workflow) applyR1Guardrails(spec *plugin.RequirementSpec, refs []string, truncNotes []string,
	digested bool, segmentCount, totalRunes int) {
	applyR1Guardrails(w, spec, refs, truncNotes, digested, segmentCount, totalRunes)
}

// digestSegment R1a：单段需求按 chunkSize 分块，逐块提炼要点后拼接（单块输出 ≤4000 字）。
// 任一块提炼失败则整段回退为截断原文，并通过 notes 留痕（→ Ambiguous → R5.3）。
func (w *Workflow) digestSegment(ctx context.Context, seg reqSegment, chunkSize int) (string, []string) {
	return digestSegment(w, ctx, seg, chunkSize)
}

func extractIssueRefs(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range issueRefRe.FindAllStringSubmatch(s, -1) {
		if seen[m[1]] || m[1] == "0" {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
		if len(out) >= maxIssueRefs {
			break
		}
	}
	return out
}

func refs2source(refs []string) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		parts = append(parts, "issue #"+r)
	}
	return strings.Join(parts, ",")
}

func renderACs(spec *plugin.RequirementSpec) string {
	var b strings.Builder
	if spec.Source != "" {
		fmt.Fprintf(&b, "需求来源：%s\n", spec.Source)
	}
	for _, ac := range spec.ACs {
		fmt.Fprintf(&b, "%s: %s\n", ac.ID, ac.Text)
	}
	if len(spec.Ambiguous) > 0 {
		b.WriteString("模糊点（改写不了，判定时如无法核实请给 unverifiable）：\n")
		for _, a := range spec.Ambiguous {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	return b.String()
}
