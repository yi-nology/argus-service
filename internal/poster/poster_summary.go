// poster_summary.go —— 总结评论四级降级定位（自 poster.go 拆出）。

package poster

import (
	"context"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

// postSummary 三级降级定位总结评论。
//
// v5.4.7 轮换浮顶语义：① 主路径定位到既有总结评论后，先比对本轮 head 与
// target.LastHeadSHA——不同（新一轮评审）→ 删旧建新（GitLab 讨论区按
// created_at 排序，原地更新永不浮顶，用户在 MR 上看不到最新报告位置）；
// 相同或 last_head_sha 缺失（同 head 重试/幂等刷新）→ 原地更新（不刷屏）。
// 删除失败降级原地更新，不阻断。
func (p *Poster) postSummary(ctx context.Context, pr plugin.PRContext, body string) (int64, error) {
	// ① review_targets.last_summary_comment_id 定位更新。
	if id, ok := p.summaryFromTarget(ctx, pr, body); ok {
		return id, nil
	}
	// ② 扫描既有评论的完整报告头（review_targets 记录损坏时的审计兜底，§10.1）。
	if id, ok := p.summaryByScan(ctx, pr, body); ok {
		return id, nil
	}
	// ③ 历史缓存兜底：② 走不到或未命中时防每轮新建堆积。
	if id, ok := p.summaryFromCached(ctx, pr, body); ok {
		return id, nil
	}
	// ④ 新建前双查（并发收口），落空才新建。
	if id, ok := p.summaryRaceRecheck(ctx, pr, body); ok {
		return id, nil
	}
	c, err := p.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body)
	if err != nil {
		return 0, err
	}
	return c.ID, nil
}

// summaryFromTarget ① 按 review_targets.last_summary_comment_id 定位既有总结
// 评论：head 推进（新一轮评审）删旧建新浮顶，相同/缺失 head 原地更新；更新失败
// 返回 false 降级走 ②/③（404 持久故障顺带清缓存定位，深挖修复）。
func (p *Poster) summaryFromTarget(ctx context.Context, pr plugin.PRContext, body string) (int64, bool) {
	target, _ := p.Store.GetReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if target == nil || target.LastSummaryCommentID <= 0 {
		return 0, false
	}
	if pr.HeadSHA != "" && target.LastHeadSHA != "" && pr.HeadSHA != target.LastHeadSHA {
		// 新一轮评审（head 推进）：删旧建新浮顶
		if id, rerr := p.rotateSummary(ctx, pr, target.LastSummaryCommentID, body); rerr == nil {
			return id, true
		} else {
			p.Log.Warn("argus.poster.summary_rotate_failed", "comment_id", target.LastSummaryCommentID,
				"error", rerr.Error(), "fallback", "update-in-place")
		}
	}
	c, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		target.LastSummaryCommentID, body)
	if err == nil {
		return p.repointIfSwapped(ctx, pr, target.LastSummaryCommentID, c), true
	}
	// 评论被删（404）是持久故障：不清缓存则每轮都先撞一次失败更新再降级。
	// 清除缓存直走 ②/③，③ 新建成功后回写自愈（深挖修复）。
	if provider.IsNotFound(err) {
		p.Log.Warn("argus.poster.summary_comment_gone", "comment_id", target.LastSummaryCommentID,
			"action", "清除缓存定位，走扫描/新建")
		if e2 := p.Store.ClearReviewTargetComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); e2 != nil {
			p.Log.Warn("argus.poster.target_clear_failed", "error", e2.Error())
		}
	} else {
		p.Log.Warn("argus.poster.summary_update_failed", "comment_id", target.LastSummaryCommentID,
			"error", err.Error(), "fallback", "scan")
	}
	return 0, false
}

// summaryByScan ② 扫描既有评论的完整报告头（review_targets 记录损坏时的审计
// 兜底，§10.1）。严格匹配 + 作者校验：宽匹配会被含该前缀的第三方评论劫持更新
// 目标。ListIssueComments 全分页收齐（go-git-platform CollectBounded，页数有界），
// 被顶出首页的报告评论也能扫描命中；列表升序取最后一个匹配（最新）。
// 扫描步保留限时：评论列表 API 卡死时宁可降级 ③/④ 也不能挂起 worker 队列。
func (p *Poster) summaryByScan(ctx context.Context, pr plugin.PRContext, body string) (int64, bool) {
	scanCtx, scanCancel := context.WithTimeout(ctx, summaryScanTimeout)
	comments, scanErr := p.SDK.ListIssueComments(scanCtx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	scanCancel()
	if scanErr != nil {
		p.Log.Warn("argus.poster.summary_scan_failed", "error", scanErr.Error(), "fallback", "create")
		return 0, false
	}
	var found int64
	for _, c := range comments {
		if !reportHeaderRe.MatchString(c.Body) {
			continue
		}
		if p.BotUsername != "" && (c.Author == nil || c.Author.Username != p.BotUsername) {
			continue
		}
		found = c.ID // 列表升序，取最后一个匹配（最新）
	}
	if found > 0 {
		c, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, found, body)
		if err == nil {
			// v3.91.2 换新语义：取返回 ID 并回写定位（同 ①）
			return p.repointIfSwapped(ctx, pr, found, c), true
		}
		p.Log.Warn("argus.poster.summary_scan_update_failed", "comment_id", found, "fallback", "create")
	}
	return 0, false
}

// summaryFromCached ③ 历史缓存兜底：② 走不到（列表 API 失败/超时）或未命中时
// 防每轮新建堆积。缓存 ID 全部是本 bot 发出的报告评论（Append 于回帖成功后记录，
// 封顶 10 条），逐个尝试直连更新，首个成功即返回。
func (p *Poster) summaryFromCached(ctx context.Context, pr plugin.PRContext, body string) (int64, bool) {
	cached, cerr := p.Store.KnownReviewTargetComments(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if cerr != nil {
		p.Log.Warn("argus.poster.known_comment_list_failed", "error", cerr.Error())
		return 0, false
	}
	var lastTried int64
	if t, gerr := p.Store.GetReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number); gerr == nil && t != nil {
		lastTried = t.LastSummaryCommentID
	}
	for _, id := range cached {
		if id == lastTried { // ① 已试过
			continue
		}
		c, err := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, id, body)
		if err == nil {
			p.Log.Info("argus.poster.summary_cached_updated", "comment_id", id, "source", "known_comment_ids")
			// v3.91.2 换新语义：取返回 ID 并回写定位（同 ①）
			return p.repointIfSwapped(ctx, pr, id, c), true
		}
	}
	return 0, false
}

// summaryRaceRecheck ④ 新建前双查（并发收口）：①②③ 落空到本步之间，同 PR 的
// 并发审查可能刚回写定位缓存——再读一次，命中即转原地更新，避免同 PR 双总结
// 评论（实测双 worker 并发审查同 PR 时的碎片化路径）。
func (p *Poster) summaryRaceRecheck(ctx context.Context, pr plugin.PRContext, body string) (int64, bool) {
	target, _ := p.Store.GetReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if target == nil || target.LastSummaryCommentID <= 0 {
		return 0, false
	}
	if c, uerr := p.SDK.UpdateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		target.LastSummaryCommentID, body); uerr == nil {
		p.Log.Info("argus.poster.summary_race_recheck", "comment_id", target.LastSummaryCommentID)
		return p.repointIfSwapped(ctx, pr, target.LastSummaryCommentID, c), true
	}
	return 0, false
}
