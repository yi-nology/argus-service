// poster_rotate.go —— 总结评论轮换与定位回写（自 poster.go 拆出）。

package poster

import (
	"context"
	"fmt"
	"time"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

// rotateSummaryFor 总结评论轮换（v5.4.7 浮顶语义）：删除上一轮总结评论并新建——
// 新建评论的 created_at 刷新，讨论区（按 created_at 排序）里最新报告浮顶。
// 删除失败或新建失败原样上抛，调用方降级原地更新（不阻断回帖主链）。
// 成功时回写定位缓存（新 comment_id + 本轮 head）。
//
// **新建**评论经副作用闸（key 含 oldID），Replayed 时取回既有新评论 ID。删除
// 留在闸外：删除失败要保留「降级原地更新」的旧语义，且 404（旧评论已不在，
// 上一轮已删/并发删除）不该阻断——继续走闸，命中重放就补回定位。
func (p *Poster) rotateSummaryFor(ctx context.Context, taskID string,
	pr plugin.PRContext, oldID int64, body string) (int64, error) {

	if err := p.SDK.DeleteIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, oldID); err != nil {
		if !provider.IsNotFound(err) {
			return 0, err
		}
		p.Log.Warn("argus.poster.summary_rotate_old_gone", "old_comment_id", oldID)
	}
	c, err := p.createCommentWithEffect(ctx, taskID,
		fmt.Sprintf("comment:rotate:%s:%s:%d", taskID, pr.HeadSHA, oldID), pr, body)
	if err != nil {
		// 旧评论已删、新评论失败：无缓存定位，下一轮走 ②扫描/④新建自愈；
		// 副作用闸的「禁止重试」错误由调用方上抛（不得降级重复创建）。
		return 0, err
	}
	if uerr := p.Store.UpsertReviewTarget(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number,
		c, pr.HeadSHA); uerr != nil {
		p.Log.Warn("argus.poster.target_repoint_failed", "comment_id", c, "error", uerr.Error())
	}
	p.Log.Info("argus.poster.summary_rotated", "old_comment_id", oldID, "new_comment_id", c,
		"head", pr.HeadSHA)
	return c, nil
}

// repointIfSwapped v3.91.2 GitLab 换新语义：sdkctx UpdateIssueComment 降级为
// CreateNote+DeleteNote 时返回新评论 ID——回写定位缓存并返回新 ID；原地更新
// （返回原 ID）时原样返回。
func (p *Poster) repointIfSwapped(ctx context.Context, pr plugin.PRContext, requested int64,
	c *model.IssueComment) int64 {
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
