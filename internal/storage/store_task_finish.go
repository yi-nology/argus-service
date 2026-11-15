// store_task_finish.go —— 任务终态收口：终态落库、归属 fencing、失败退避与预留核销。

package storage

import (
	"context"
)

// UpdateTaskRunningStatus 局部更新状态字段（任务执行中的落点）。
func (s *Store) FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) error {
	return finishTask(s, ctx, id, status, verdict, report, resultJSON, failReason, tokensUsed, summaryCommentID)
}

// FinishTaskFenced 终态落库，带执行归属 fencing（v3.8.0）：
// WHERE 追加 status='running'（+ runner_id 归属校验）——stale 重置后旧执行体
// 的僵尸写入直接失效，不再覆盖新执行体的结果/引发双评论。此前是无守卫的
// Where("id = ?")，后写者全列覆盖先写者。返回 fenced=true 表示本次写入因
// 归属失效被拒（调用方告警；属预期行为而非错误）。
// 例外：RunnerID 未装配（单测桩）时只校验 status。
func (s *Store) FinishTaskFenced(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) (fenced bool, err error) {
	return finishTaskFenced(s, ctx, id, status, verdict, report, resultJSON, failReason, tokensUsed, summaryCommentID)
}
