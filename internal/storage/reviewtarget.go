// reviewtarget.go —— review_targets 表操作面（v6.2.0 归位：Get/Upsert 自
// store.go 迁入，全表操作单文件）。Upsert 的语义是">0 才写"（防 GORM 零值
// 误覆盖），而"总结评论已被删除"需要显式清零缓存定位（poster ① 级 404 自愈，
// 深挖修复）——传 0 复用 Upsert 是 no-op，清不掉（ClearReviewTargetComment）。

package storage

import (
	"context"
)

// ClearReviewTargetComment 显式清零 last_summary_comment_id（评论 404 后调用），
// head SHA 不动（同 SHA 去重语义保留）。行不存在视为已清除。
func (s *Store) ClearReviewTargetComment(ctx context.Context, platform, owner, repo, number string) error {
	return clearReviewTargetComment(s, ctx, platform, owner, repo, number)
}

// knownCommentIDsCap 历史报告评论 ID 缓存上限（postSummary 四级降级的兜底环：
// 定位缓存丢失或评论列表 API 故障时，缓存 ID 可直连更新）。
const knownCommentIDsCap = 10

// AppendReviewTargetComment 记录一条本 bot 发出的总结评论 ID（去重、保序、封顶，
// 超出淘汰最旧）。与 LastSummaryCommentID 分开：后者是"最新定位"，前者是历史全量。
func (s *Store) AppendReviewTargetComment(ctx context.Context, platform, owner, repo, number string, id int64) error {
	return appendReviewTargetComment(s, ctx, platform, owner, repo, number, id)
}

// KnownReviewTargetComments 返回缓存的历史评论 ID（新→旧排列供逐个尝试；无缓存返回 nil）。
func (s *Store) KnownReviewTargetComments(ctx context.Context, platform, owner, repo, number string) ([]int64, error) {
	return knownReviewTargetComments(s, ctx, platform, owner, repo, number)
}

// GetReviewTarget 查询（不存在返回 nil,nil）。
func (s *Store) GetReviewTarget(ctx context.Context, platform, owner, repo, number string) (*ReviewTarget, error) {
	return getReviewTarget(s, ctx, platform, owner, repo, number)
}

// UpsertReviewTarget 回帖成功后更新定位（staleness 不读它，五轮评审定案）。
func (s *Store) UpsertReviewTarget(ctx context.Context, platform, owner, repo, number string,
	summaryCommentID int64, headSHA string) error {
	return upsertReviewTarget(s, ctx, platform, owner, repo, number, summaryCommentID, headSHA)
}
