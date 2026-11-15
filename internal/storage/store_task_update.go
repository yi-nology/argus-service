// store_task_update.go —— 任务窄写口：修复状态/head/debug/已发评审标记。

package storage

import "context"

// GetTaskByEventID webhook 幂等查询：命中 = 平台重发。无此任务返回 ErrNotFound
// 哨兵（v6.2.0 补契约：此前裸透 gorm.ErrRecordNotFound，违反"ORM 错误语义
// 不出 storage 包"——两个消费点均按非 nil 即未命中处理，行为不变）。
// UpdateTaskFixStatus 修复链状态落库（Phase 3 状态机窄写口：只动 fix_status
// 一列，不经通用 Update 防带出并发写冲突面）。
func (s *Store) UpdateTaskFixStatus(ctx context.Context, id, fixStatus string) error {
	return updateTaskFixStatus(s, ctx, id, fixStatus)
}

// SetTaskHeadSHA 回填 CLI 任务的 head SHA（执行期从平台获取）。
func (s *Store) SetTaskHeadSHA(ctx context.Context, id, sha string) error {
	return setTaskHeadSHA(s, ctx, id, sha)
}

// SetTaskDebug 留存阶段调试产物（ARGUS_DEBUG=1）。
func (s *Store) SetTaskDebug(ctx context.Context, id, debugJSON string) error {
	return setTaskDebug(s, ctx, id, debugJSON)
}

// MarkReviewPosted 完整评审发成功落标记（v3.36.0）：重跑任务据此跳过
// ListReviews 平台锚点查询（总结评论更新不受影响，照常执行）。
func (s *Store) MarkReviewPosted(ctx context.Context, id string) error {
	return markReviewPosted(s, ctx, id)
}
