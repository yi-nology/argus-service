// poster_rotate.go —— 总结评论轮换与定位回写（自 poster.go 拆出）。

package poster

import (
	"context"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

// rotateSummary 总结评论轮换（v5.4.7 浮顶语义）：删除上一轮总结评论并新建——
// 新建评论的 created_at 刷新，讨论区（按 created_at 排序）里最新报告浮顶。
// 删除失败或新建失败原样上抛，调用方降级原地更新（不阻断回帖主链）。
// 成功时回写定位缓存（新 comment_id + 本轮 head）。
func (p *Poster) rotateSummary(ctx context.Context, pr plugin.PRContext, oldID int64, body string) (int64, error) {
	if err := p.SDK.DeleteIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, oldID); err != nil {
		return 0, err
	}
	c, err := p.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body)
	if err != nil {
		// 旧评论已删、新评论失败：无缓存定位，下一轮走 ②扫描/④新建自愈
		return 0, err
	}
	if uerr := p.Store.UpsertReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		c.ID, pr.HeadSHA); uerr != nil {
		p.Log.Warn("argus.poster.target_repoint_failed", "comment_id", c.ID, "error", uerr.Error())
	}
	p.Log.Info("argus.poster.summary_rotated", "old_comment_id", oldID, "new_comment_id", c.ID,
		"head", pr.HeadSHA)
	return c.ID, nil
}

// repointIfSwapped v3.91.2 GitLab 换新语义：sdkctx UpdateIssueComment 降级为
// CreateNote+DeleteNote 时返回新评论 ID——回写定位缓存并返回新 ID；原地更新
// （返回原 ID）时原样返回。
func (p *Poster) repointIfSwapped(ctx context.Context, pr plugin.PRContext, requested int64,
	c *provider.IssueComment) int64 {
	if c == nil || c.ID <= 0 || c.ID == requested {
		return requested
	}
	if uerr := p.Store.UpsertReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		c.ID, pr.HeadSHA); uerr != nil {
		p.Log.Warn("argus.poster.target_repoint_failed", "comment_id", c.ID, "error", uerr.Error())
	}
	return c.ID
}

// summaryScanTimeout 总结评论定位（扫描既有评论）步的时间上限：
// 该步若卡死会阻塞整个 worker，宁可降级新建评论也不能挂起队列。
const summaryScanTimeout = 60 * time.Second
