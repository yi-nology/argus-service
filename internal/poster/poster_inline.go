// poster_inline.go —— 行内意见筛选与评审事件投递（自 poster.go 拆出）。

package poster

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/oagentkit/guardx/fence"
	"github.com/oagentkit/textx/reportutil"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
)

// prepareInline 行内意见筛选与评论构造：f.Line>0 且有 file 的 finding 进候选；
// file 不在 diff 文件列表内的剔除（模型幻觉路径直发会被平台整单拒绝），精确 miss
// 时唯一尾段匹配修正（v3.51.0 路径归一化）；剔除数追加进 rep.Degraded 用户可见。
// 返回按严重度排序、经 max_comments 钳制后的评论表。
func (p *Poster) prepareInline(task *storage.Task, diff *plugin.DiffContext, rep *reportview.Report) []model.ReviewComment {
	var inlineFindings []domain.Finding
	for _, f := range rep.Findings {
		if f.Line > 0 && f.File != "" {
			inlineFindings = append(inlineFindings, f)
		}
	}
	// 行内预校验：file 必须在 diff 文件列表内。LLM 幻觉文件/行号直发会被平台
	// 整单拒绝（422），连累评审事件与全部行内意见一起丢——被剔除的留在总结评论里。
	// v3.51.0 路径归一化兜底：模型常以子项目根为参照报路径、缺 git 仓根前缀
	//（实测 demo-insights/{agent,server} 两仓 100% 复现，行内评论因此从未命中）。
	// 精确 miss 时在 Included 文件中做唯一尾段匹配（/ 段边界），唯一命中才修正——
	// 0/多命中维持剔除（错发位置比不发更糟）。剔除数进 rep.Degraded 用户可见。
	dropped := 0
	if diff != nil && len(inlineFindings) > 0 {
		inDiff := make(map[string]bool, len(diff.Files))
		var included []string
		for _, df := range diff.Files {
			inDiff[df.Path] = true
			if df.Included {
				included = append(included, df.Path)
			}
		}
		kept := inlineFindings[:0]
		for _, f := range inlineFindings {
			if inDiff[f.File] {
				kept = append(kept, f)
			} else if fixed, ok := uniqueSuffixMatch(included, f.File); ok {
				p.Log.Warn("argus.poster.inline_path_normalized",
					"task", task.ID, "from", f.File, "to", fixed, "line", f.Line)
				f.File = fixed
				kept = append(kept, f)
			} else {
				dropped++
				p.Log.Warn("argus.poster.inline_dropped_not_in_diff",
					"task", task.ID, "file", f.File, "line", f.Line)
			}
		}
		inlineFindings = kept
		if dropped > 0 {
			rep.Degraded = append(rep.Degraded, fmt.Sprintf(
				"%d 条意见的文件路径无法对应 diff 中的文件（模型路径引用偏差），仅在总结中展示、未行内标注", dropped))
		}
	}
	slices.SortStableFunc(inlineFindings, func(a, b domain.Finding) int {
		return -cmp.Compare(reportutil.Rank(a.Severity), reportutil.Rank(b.Severity))
	})
	inlineFindings = clampInlineByMaxComments(inlineFindings, rep)
	comments := make([]model.ReviewComment, 0, len(inlineFindings))
	for _, f := range inlineFindings {
		comments = append(comments, model.ReviewComment{
			Path: f.File, Line: f.Line,
			Body: inlineCommentBody(f),
		})
	}
	return comments
}

// sendReview 评审事件投递：verdict 映射评审事件；幂等锚点查重（库标记短路 → 平台
// 锚点查询回退）；CreateReview 三级降级（blocking 拒绝 → COMMENT → 无行内意见纯评论）。
// 返回实际投递的行内评论条数——主路径与 COMMENT 降级（仍带 comments）为 len(comments)，
// 无行内兜底为 0，幂等跳过与全失败也为 0。
func (p *Poster) sendReview(ctx context.Context, pr plugin.PRContext, rep reportview.Report,
	verdict string, comments []model.ReviewComment) int {
	// verdict 映射评审事件：request_changes→REQUEST_CHANGES / comment→COMMENT / approve→APPROVE。
	// 有行内 findings 时一并附上；无行内意见也提交评审事件（否则 PR 页看不到任何审查动作，
	// 评审状态恒为"暂无评审员"——真实试点暴露的可用性缺陷）。
	event := reviewEventFor(verdict)
	reviewAnchor := "\n\n<!-- argus:review task_id=" + rep.TaskID + " head=" + rep.HeadSHA + " verdict=" + verdict + " -->"
	// v6.1.5 结论 TL;DR 先行：VerdictText 行后插入结论摘要区（分级计数 + 重点关注
	// top3，无意见退化为单行），用户在评审事件上先看结论再看总结评论详报。
	// inlineCount 取本次评审实附的行内评论数（主路径与 COMMENT 降级路径一致；末级
	// 「无行内兜底」路径评审未带行内意见，计数与事实的偏差随该降级日志可查——
	// body 在投递前构造，不做回改）。
	reviewBody := "Argus 审查结论：" + rep.VerdictText + "\n\n" +
		reviewTLDR(rep, len(comments)) + "\n\n完整报告见总结评论（该评论随每次同步更新）。" + reviewAnchor
	// 评审幂等锚点查重（库标记短路 → 平台锚点查询回退，详见 helper 注释）。
	if p.reviewAlreadyPosted(ctx, pr, rep) {
		return 0
	}
	// CreateReview 三级降级投递（平台策略拒绝 → COMMENT → 无行内意见纯评论）。
	reviewSent, postedInline := p.createReviewWithFallback(ctx, pr, rep, event, reviewBody, comments)
	if reviewSent {
		// 发成功落库标记（v3.36.0）：重跑短路平台锚点查询
		if merr := p.Store.MarkReviewPosted(ctx, rep.TaskID); merr != nil {
			p.Log.Warn("argus.poster.mark_posted_failed", "error", merr.Error(),
				"hint", "未落库标记：重跑回退 ListReviews 锚点查询（幂等不丢）")
		}
		return postedInline
	}
	return 0
}

// reviewEventFor verdict → 评审事件词表：request_changes→REQUEST_CHANGES /
// comment→COMMENT / approve→APPROVE（其余缺省 COMMENT）。
func reviewEventFor(verdict string) string {
	switch verdict {
	case "request_changes":
		return "REQUEST_CHANGES"
	case "approve":
		return "APPROVE"
	}
	return "COMMENT"
}

// reviewAlreadyPosted 评审幂等锚点查重：任务模型 at-least-once（崩溃/硬取消落在
// 发评审与落终态之间 → 重跑），重跑会再发一条完整评审（行内评论重复）。锚点只写
// 不查等于没有——发前按 task_id 查一次；查询失败不阻断正常路径（宁多查一次 API，
// 不因兜底接口缺失丢评审）。
// v3.36.0：review_posted 库标记短路——此前重跑任务必 ListReviews 拉全量评审
// （大 PR 有延迟）。查库失败/未标记时回退平台锚点查询（宁多查一次）。
func (p *Poster) reviewAlreadyPosted(ctx context.Context, pr plugin.PRContext, rep reportview.Report) bool {
	alreadyPosted := false
	if t, gerr := p.Store.GetTask(ctx, rep.TaskID); gerr == nil && t != nil && t.ReviewPosted {
		alreadyPosted = true
		p.Log.Info("argus.poster.review_skip_by_flag", "task", rep.TaskID,
			"action", "跳过重复评审（review_posted；总结评论照常更新）")
	}
	if posted, err := p.SDK.ListReviews(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); err == nil && !alreadyPosted {
		for _, r := range posted {
			if strings.Contains(r.Body, "argus:review task_id="+rep.TaskID+" ") {
				alreadyPosted = true
				p.Log.Warn("argus.poster.review_already_posted",
					"task", rep.TaskID, "review_id", r.ID, "action", "跳过重复评审（总结评论照常更新）")
				break
			}
		}
	}
	return alreadyPosted
}

// createReviewWithFallback CreateReview 三级降级投递：主路径被平台策略拒绝
// （真实试点发现 PR 作者本人不能给自己提 blocking 评审）→ COMMENT 评论 →
// 无行内意见纯评论兜底。返回是否投递成功与实附行内评论条数（末级兜底成功为 0）。
func (p *Poster) createReviewWithFallback(ctx context.Context, pr plugin.PRContext,
	rep reportview.Report, event, reviewBody string, comments []model.ReviewComment) (bool, int) {
	reviewSent := false
	postedInline := 0
	if _, err := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		provider.CreateReviewOptions{
			CommitID: pr.HeadSHA,
			Event:    event,
			Body:     reviewBody,
			Comments: comments,
		}); err != nil {
		// 平台策略降级（真实试点发现）：PR 作者本人不能给自己提 blocking 评审
		//（Forgejo/Gitea "reject your own pull is not allowed"）。生产部署应使用
		// 独立 bot 账号；共用账号时自动降级为 COMMENT——审查记录仍然落地，
		// 结论语义保留在评审 body 与总结评论中。
		p.Log.Warn("argus.poster.review_failed", "error", err.Error(), "event", event,
			"fallback", "COMMENT 评审")
		fallbackBody := "⚠️ 平台限制：当前账号与 PR 作者相同，无法提交 " + event +
			" 评审，已降级为评论。原判定：" + rep.VerdictText + "\n\n" + reviewBody
		if _, err2 := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
			provider.CreateReviewOptions{
				CommitID: pr.HeadSHA,
				Event:    "COMMENT",
				Body:     fallbackBody,
				Comments: comments,
			}); err2 != nil {
			// 行内意见可能整体非法（如全部行号越界）——去掉 comments 以纯评论事件兜底，
			// 保住评审记录与结论语义（此前整轮评审事件直接丢失）。
			p.Log.Warn("argus.poster.review_fallback_failed", "error", err2.Error(),
				"fallback", "无行内意见评审")
			if _, err3 := p.SDK.CreateReview(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
				provider.CreateReviewOptions{
					CommitID: pr.HeadSHA,
					Event:    "COMMENT",
					Body:     fallbackBody,
				}); err3 != nil {
				p.Log.Error("argus.poster.review_bare_failed", "error", err3.Error())
				plugin.ReportPluginFailure("poster:review")
			} else {
				reviewSent = true // 无行内兜底：postedInline 保持 0
			}
		} else {
			reviewSent = true
			postedInline = len(comments)
		}
	} else {
		reviewSent = true
		postedInline = len(comments)
	}
	return reviewSent, postedInline
}

// reviewTLDRMaxTop TL;DR「重点关注」最多列出的意见条数（severity 降序取顶）。
const reviewTLDRMaxTop = 3

// reviewTLDRSummaryRunes 单条重点摘要的 rune 上限（首行截取后再截断）。
const reviewTLDRSummaryRunes = 80

// reviewTLDR 评审 body 的结论摘要区（v6.1.5「结论 TL;DR 先行」）：有意见时给
// 分级计数 + 重点关注（severity 降序、同级别按报告序，最多 3 条，file:line +
// Comment 首行截 ~80 rune）；无意见退化为单行「✅ 未发现问题」。只消费
// reportview.Report 既有字段，纯函数独立可测。
func reviewTLDR(rep reportview.Report, inlineCount int) string {
	if len(rep.Findings) == 0 {
		return "✅ 未发现问题"
	}
	bySev := map[string]int{}
	for _, f := range rep.Findings {
		bySev[f.Severity]++
	}
	var b strings.Builder
	b.WriteString("意见：")
	// 计数行按 design §3.3 三档词表固定展示；critical（textx#20 四档预留，当前
	// 管线不产出）非零时前置列出，保证「共 N 条」与分档之和恒一致（防御性）。
	if n := bySev[reportutil.Critical]; n > 0 {
		fmt.Fprintf(&b, "critical %d · ", n)
	}
	fmt.Fprintf(&b, "high %d · medium %d · low %d（共 %d 条，行内已标注 %d 条）",
		bySev[plugin.SeverityHigh], bySev[plugin.SeverityMedium], bySev[plugin.SeverityLow],
		len(rep.Findings), inlineCount)
	// 重点关注：排序纪律与 prepareInline 一致（Rank 降序 + 稳定排序保报告序）。
	// 摘要是不可信模型文本，经 fence.EscapeUntrusted 注入中和后展示。
	top := append([]domain.Finding(nil), rep.Findings...)
	slices.SortStableFunc(top, func(a, b domain.Finding) int {
		return -cmp.Compare(reportutil.Rank(a.Severity), reportutil.Rank(b.Severity))
	})
	if len(top) > reviewTLDRMaxTop {
		top = top[:reviewTLDRMaxTop]
	}
	b.WriteString("\n重点关注：")
	for _, f := range top {
		fmt.Fprintf(&b, "\n• [%s] ", strings.ToUpper(f.Severity))
		if f.File != "" {
			fmt.Fprintf(&b, "%s:%d ", fence.EscapeUntrusted(f.File), f.Line)
		}
		b.WriteString(fence.EscapeUntrusted(textutil.TruncEllipsis(
			tldrFirstLine(f.Comment), reviewTLDRSummaryRunes)))
	}
	return b.String()
}

// tldrFirstLine 摘要首行截取（\n / \r\n 均按行终止，去尾空白）。
func tldrFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// inlineCommentBody 拼行内评论正文：严重级 + 意见 + 修复建议。
// SuggestedFix 非空时渲染为 ```suggestion 围栏（Gitea/GitHub 渲染出"一键应用"按钮），
// 建议内容是代码原文，不做 HTML 转义——否则 Apply 后代码被污染。围栏安全：
// 建议含 ``` 或超长时退化为纯文本意见（完整建议仍在总结评论中展示），不让围栏断裂。
func inlineCommentBody(f domain.Finding) string {
	body := "**[" + strings.ToUpper(f.Severity) + "]** " + fence.EscapeUntrusted(f.Comment)
	if ann := dispatch.SamplesAnnotation(f); ann != "" {
		// best-of-N 复现计数（结构化字段渲染，非评语后缀——指纹身份不受影响）
		body += ann
	}
	if ann := dispatch.ConfidenceAnnotation(f); ann != "" {
		body += ann
	}
	if f.Verified {
		body += "（✅ 沙箱验证通过）"
	}
	fix := strings.TrimSpace(f.SuggestedFix)
	if fix != "" && !strings.Contains(fix, "```") {
		if _, truncated := textutil.TruncRunes(fix, suggestionMaxRunes); !truncated {
			body += "\n\n```suggestion\n" + fix + "\n```"
		} else {
			body += "\n\n（修复建议过长，已省略；见总结评论）"
		}
	}
	return body + "\n\n<!-- argus:inline by=" + f.By + " -->"
}

// suggestionMaxRunes 单条 suggestion 围栏的建议长度上限（防超大评论刷屏）。
const suggestionMaxRunes = 1200

// clampInlineByMaxComments 仓库级行内评论上限（v3.15.0 review.max_comments；
// 0 = 不限）：调用方已按严重度降序排好，这里保留 top N，其余仅在总结评论列出
// （Findings 段完整，透明可查），超限事实以降级注明写入报告。
func clampInlineByMaxComments(inlineFindings []domain.Finding, rep *reportview.Report) []domain.Finding {
	if rep.MaxInlineComments <= 0 || len(inlineFindings) <= rep.MaxInlineComments {
		return inlineFindings
	}
	dropped := len(inlineFindings) - rep.MaxInlineComments
	rep.Degraded = append(rep.Degraded, fmt.Sprintf(
		"仓库配置 review.max_comments=%d：%d 条行内意见超出上限，仅在总结评论列出",
		rep.MaxInlineComments, dropped))
	return inlineFindings[:rep.MaxInlineComments]
}

// uniqueSuffixMatch 在 paths 中找 file 的唯一尾段匹配（/ 段边界，防 pcap.go 误配
// pcapDownload.go）。模型路径幻觉的常见形态是缺 git 仓根前缀（子项目根参照）——
// 唯一命中返回修正路径；0/多命中返回 false（多义时错发位置比不发更糟）。
func uniqueSuffixMatch(paths []string, file string) (string, bool) {
	if file == "" {
		return "", false
	}
	hit := ""
	for _, p := range paths {
		if p == file || strings.HasSuffix(p, "/"+file) {
			if hit != "" {
				return "", false
			}
			hit = p
		}
	}
	return hit, hit != ""
}
