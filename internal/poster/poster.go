// Package poster 回帖服务（design §7-7 / §10.1 / §10.2）：
//   - staleness 门控：存在同 PR 更晚创建的非 failed 任务 ⇒ 本任务 stale（不回帖、不覆盖 review_targets）；
//   - 行内评论随 CreateReview 每轮独立，不追平；
//   - 总结评论 synchronize 时更新既有评论（review_targets.last_summary_comment_id 定位），
//     失败降级为扫描 HTML 注释头（§10.1 审计锚点），再降级新建；
//   - 回帖成功后更新 review_targets（last_summary_comment_id + last_head_sha）。
package poster

import (
	"context"
	"regexp"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
)

// Poster 回帖执行器。
type Poster struct {
	SDK     *argusctx.SDKCtx
	Store   *storage.Store
	Version string
	Log     logx.Logger
	// BotUsername bot 账号名（扫描兜底时校验报告头评论作者；空 = 不校验作者）
	BotUsername string
	// Effects 副作用闸（C1；nil = 按 Store+ARGUS_SIDEEFFECT_MODE 惰性装配）。
	// 创建型评论（新建/轮换新建）经它保证至多一次；按 ID 的 update 不走闸。
	Effects *effect.Runner
}

// reportHeaderRe 严格报告头：字段齐全且独占一行（head 允许为空——手动任务回填失败时
// 报告头是 head=）。宽匹配 Contains 会被含该前缀的第三方评论劫持更新目标。
var reportHeaderRe = regexp.MustCompile(`(?m)^<!-- argus:report task_id=\S+ head=\S* ver=\S+ -->[ \t]*$`)

// Result 回帖结果。
type Result struct {
	Stale            bool // 命中 staleness：未回帖
	SummaryCommentID int64
	InlinePosted     int
}

// Post 执行回帖。报告内容由 runner 组装成 reportview.Report（本包只管发到哪里、怎么降级）。
// diff 用于行内评论预校验（可为 nil = 跳过校验）。
func (p *Poster) Post(ctx context.Context, task *storage.Task, pr plugin.PRContext,
	diff *plugin.DiffContext, verdict string, rep reportview.Report) (*Result, error) {

	// ---- staleness 门控（§10.2 单条件判定）----
	stale, err := p.Store.HasNewerNonFailedTask(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		task.ID, task.CreatedAt)
	if err != nil {
		return nil, err
	}
	if stale {
		p.Log.Warn("argus.poster.stale_skip",
			"task", task.ID, "pr", pr.Owner+"/"+pr.Repo+"#"+pr.Number,
			"head", pr.HeadSHA, "reason", "存在同 PR 更晚创建的非 failed 任务")
		return &Result{Stale: true}, nil
	}

	// ---- 行内意见准备（过滤/路径归一化/排序/钳制/构造评论）----
	comments := p.prepareInline(task, diff, &rep)

	// ---- 评审事件（幂等 + 三级降级）----
	inline := p.sendReview(ctx, pr, rep, verdict, comments)

	// ---- 总结评论：更新 → 扫描兜底 → 历史缓存兜底 → 新建（四级降级）----
	body := reportview.Render(rep)
	commentID, err := p.postSummaryFor(ctx, task.ID, pr, body)
	if err != nil {
		plugin.ReportPluginFailure("poster")
		return nil, err
	}

	// ---- 回帖成功：更新 review_targets（staleness 不读它；同 SHA 去重读 last_head_sha）----
	if err := p.Store.UpsertReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		commentID, pr.HeadSHA); err != nil {
		p.Log.Error("argus.poster.target_update_failed", "error", err.Error())
	}
	// 历史评论 ID 缓存追加（收尾批修复）：定位缓存丢失（review_targets 记录损坏）
	// 或评论列表 API 故障时的直连更新兜底，防每轮新建报告评论
	if err := p.Store.AppendReviewTargetComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, commentID); err != nil {
		p.Log.Warn("argus.poster.known_comment_append_failed", "error", err.Error())
	}

	return &Result{SummaryCommentID: commentID, InlinePosted: inline}, nil
}
