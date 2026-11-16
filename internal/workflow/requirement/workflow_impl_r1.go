// workflow_impl_r1.go —— Workflow.R1 需求解析的包级实现（R1/R2 准备面）。

package requirement

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/textx/textutil"
	"github.com/oekit/pkg/jsonrepair"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/go-git-platform/provider"
)

// ParseRequirement 的包级实现（转发壳见原文件）。
func parseRequirement(w *Workflow, ctx context.Context, task plugin.Task) (*plugin.RequirementSpec, error) {
	start := time.Now()
	defer func() {
		plugin.ReportStageDuration(plugin.LayerR1, time.Since(start))
		plugin.ReportLayerExec(plugin.LayerR1)
	}()
	w.onStage(plugin.LayerR1)

	bodyMax, issueMax := w.ReqBodyMaxChars, w.ReqIssueMaxChars
	if bodyMax <= 0 {
		bodyMax = defaultReqBodyMaxChars
	}
	if issueMax <= 0 {
		issueMax = defaultReqIssueMaxChars
	}
	digestMax := bodyMax * 8

	pr := task.PR
	segments, refs, truncNotes := w.collectReqSegments(ctx, pr, digestMax, issueMax)

	total := 0
	for _, seg := range segments {
		total += utf8.RuneCountInString(seg.content)
	}
	if total == 0 && strings.TrimSpace(pr.HeadBranch) == "" {
		// 需求源全缺：task 照走，R1 产出空清单 + Ambiguous=[原文] → R5.3 → COMMENT（§10.2）
		return &plugin.RequirementSpec{
			Source:    "未关联",
			Ambiguous: []string{"未在 PR 描述、分支名中找到关联需求，请补充需求说明"},
		}, nil
	}

	// 组装 R1 输入：总量 ≤ bodyMax 原文直进；超过则两级提炼
	var sb strings.Builder
	if pr.HeadBranch != "" {
		sb.WriteString(DataFence("分支名", pr.HeadBranch))
		sb.WriteString("\n")
	}
	digested := false
	if total > bodyMax {
		digested = true
		for _, seg := range segments {
			digest, notes := w.digestSegment(ctx, seg, bodyMax)
			truncNotes = append(truncNotes, notes...)
			fmt.Fprintf(&sb, "%s\n", DataFence(seg.label+"（分段提炼要点）", digest))
		}
	} else {
		for _, seg := range segments {
			fmt.Fprintf(&sb, "%s\n", DataFence(seg.label, seg.content))
		}
	}

	var spec plugin.RequirementSpec
	msgs := []*schema.Message{
		schema.SystemMessage(r1System),
		schema.UserMessage(sb.String() + "\n请解析以上需求信息，输出验收清单 JSON。"),
	}
	// 直调 + llmjson 宽容解析（深挖修复）：此前走 GenerateJSON 纯严格链，中文模型
	// 高频的全角标点/尾逗号类损坏会把整条 PR 打成「解析降级」→ 全 Ambiguous →
	// R5.3 强制 COMMENT——最贵的失败模式。R3/R3.5 已迁移，R1 补齐。
	resp, err := w.LLM.Generate(ctx, plugin.LayerR1, msgs)
	if err == nil {
		if e := jsonrepair.Unmarshal(plugin.NormalizeFence(resp.Content), &spec); e != nil {
			err = e
		}
	}
	if err != nil {
		w.Log.Warn("argus.r1.degrade", "error", err.Error())
		// 解析降级：全部当 ambiguous → 人工确认路径（结果不丢）
		return &plugin.RequirementSpec{
			Source:    "解析降级",
			Ambiguous: []string{"需求解析失败，需人工确认：" + truncSummary(sb.String(), 300)},
		}, nil
	}
	w.applyR1Guardrails(&spec, refs, truncNotes, digested, len(segments), total)
	return &spec, nil
}

// collectReqSegments 的包级实现（转发壳见原文件）。
func collectReqSegments(w *Workflow, ctx context.Context, pr plugin.PRContext,
	digestMax, issueMax int) (segments []reqSegment, refs []string, truncNotes []string) {
	segments = make([]reqSegment, 0, 6)
	if s := strings.TrimSpace(pr.Body); s != "" {
		seg := reqSegment{label: "PR 描述", content: s, rawChars: utf8.RuneCountInString(s)}
		if t, truncated := textutil.TruncRunes(s, digestMax); truncated {
			seg.content, seg.truncated = t, true
			truncNotes = append(truncNotes, fmt.Sprintf("PR 描述超长已截断（原 %d 字符，保留前 %d），请人工核对完整性", seg.rawChars, digestMax))
		}
		segments = append(segments, seg)
	}

	// 关联 issue：分支名 + 描述中 #N（去重，封顶）
	refs = extractIssueRefs(pr.HeadBranch + " " + pr.Body)
	for _, num := range refs {
		issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, num)
		if err != nil {
			if provider.IsNotFound(err) {
				continue
			}
			w.Log.Warn("argus.r1.issue_fetch_failed", "issue", num, "error", err.Error())
			continue
		}
		raw := issue.Title + "\n" + issue.Body // 标题同样攻击者可控，一起进数据区
		seg := reqSegment{label: "issue #" + num, content: raw, rawChars: utf8.RuneCountInString(raw)}
		if t, truncated := textutil.TruncRunes(raw, issueMax*8); truncated {
			seg.content, seg.truncated = t, true
			truncNotes = append(truncNotes, fmt.Sprintf("issue #%s 正文超长已截断（原 %d 字符，保留前 %d），请人工核对完整性", num, seg.rawChars, issueMax*8))
		}
		segments = append(segments, seg)
	}
	return segments, refs, truncNotes
}

// applyR1Guardrails 的包级实现（转发壳见原文件）。
func applyR1Guardrails(w *Workflow, spec *plugin.RequirementSpec, refs []string, truncNotes []string,
	digested bool, segmentCount, totalRunes int) {
	if spec.Source == "" {
		spec.Source = textutil.FirstNonEmpty(refs2source(refs), "PR 描述")
	}
	if warnings := SanitizeSpec(spec); len(warnings) > 0 {
		for _, wn := range warnings {
			w.Log.Warn("argus.r1.injection_suspect", "warning", wn)
		}
	}
	// 补充关联 issue 引用便于追溯
	spec.IssueRefs = refs
	// 截断/两级提炼留痕：Ambiguous 非空 → R5.3 COMMENT（人工确认），绝不静默
	for _, n := range truncNotes {
		spec.Ambiguous = append(spec.Ambiguous, "[超长截断] "+n)
		w.Log.Warn("argus.r1.requirement_truncated", "note", n)
	}
	if digested {
		spec.Ambiguous = append(spec.Ambiguous, "[两级提炼] 需求原文超长，已按分段提炼要点后解析，建议人工核对需求完整性")
		w.Log.Warn("argus.r1.two_stage_digest", "segments", segmentCount, "total_chars", totalRunes)
	}
	// 防线 #3：空清单也要有出处
	if len(spec.ACs) == 0 && len(spec.Ambiguous) == 0 {
		spec.Ambiguous = append(spec.Ambiguous, "需求信息未能改写出可判定条目，需人工确认")
	}
	// AC 条数上限：超限条目不丢弃，转 Ambiguous 落人工确认
	if len(spec.ACs) > maxACs {
		excess := spec.ACs[maxACs:]
		ids := make([]string, 0, len(excess))
		for _, ac := range excess {
			ids = append(ids, ac.ID)
		}
		w.Log.Warn("argus.r1.ac_cap", "total", len(spec.ACs), "cap", maxACs)
		spec.Ambiguous = append(spec.Ambiguous, fmt.Sprintf(
			"[条目超限] 验收清单解析出 %d 条，超出 %d 条上限，多余条目（%s）转人工确认，建议拆分需求",
			len(spec.ACs), maxACs, strings.Join(ids, ",")))
		spec.ACs = spec.ACs[:maxACs]
	}
	// 单条 AC 文本截断（LLM 偶发把整段原文塞进单条 AC）
	for i := range spec.ACs {
		spec.ACs[i].Text = textutil.TruncNote(spec.ACs[i].Text, maxACTextRunes, "截断")
	}
}

// digestSegment 的包级实现（转发壳见原文件）。
func digestSegment(w *Workflow, ctx context.Context, seg reqSegment, chunkSize int) (string, []string) {
	var notes []string
	chunks := textutil.SplitRunes(seg.content, chunkSize)
	if len(chunks) > maxDigestChunks {
		notes = append(notes, fmt.Sprintf("%s 超长（原 %d 字符），仅提炼前 %d 块（每块 %d 字符），请人工核对完整性",
			seg.label, seg.rawChars, maxDigestChunks, chunkSize))
		chunks = chunks[:maxDigestChunks]
	}
	parts := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		msgs := []*schema.Message{
			schema.SystemMessage(r1DigestSystem),
			schema.UserMessage(DataFence(seg.label+fmt.Sprintf(" 需求片段 %d/%d（数据）", i+1, len(chunks)), ch) +
				"\n请提炼以上需求片段的全部要点。"),
		}
		resp, err := w.LLM.Generate(ctx, plugin.LayerR1A, msgs)
		if err != nil {
			w.Log.Warn("argus.r1.digest_failed", "segment", seg.label, "chunk", i+1, "error", err.Error())
			notes = append(notes, fmt.Sprintf("%s 片段提炼失败（块 %d/%d），以截断原文参与解析", seg.label, i+1, len(chunks)))
			// 块级回退（深挖修复）：此前任一块失败即整段回退 seg.content——上限
			// bodyMax×8（缺省 192k 字符），两级提炼的控量目标被单块失败击穿，
			// 只剩 oagentkit/llm fitInput 静默截断兜底。只回退失败块并截到块长。
			parts = append(parts, truncSummary(ch, chunkSize))
			continue
		}
		parts = append(parts, truncSummary(resp.Content, 4000))
	}
	return strings.Join(parts, "\n"), notes
}
