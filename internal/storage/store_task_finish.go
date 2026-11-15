// store_task_finish.go —— 任务终态收口：终态落库、归属 fencing、失败退避与预留核销。

package storage

import (
	"context"
	"time"
)

// UpdateTaskRunningStatus 局部更新状态字段（任务执行中的落点）。
func (s *Store) FinishTask(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) error {
	_, err := s.FinishTaskFenced(ctx, id, status, verdict, report, resultJSON, failReason, tokensUsed, summaryCommentID)
	return err
}

// FinishTaskFenced 终态落库，带执行归属 fencing（v3.8.0）：
// WHERE 追加 status='running'（+ runner_id 归属校验）——stale 重置后旧执行体
// 的僵尸写入直接失效，不再覆盖新执行体的结果/引发双评论。此前是无守卫的
// Where("id = ?")，后写者全列覆盖先写者。返回 fenced=true 表示本次写入因
// 归属失效被拒（调用方告警；属预期行为而非错误）。
// 例外：RunnerID 未装配（单测桩）时只校验 status。
func (s *Store) FinishTaskFenced(ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) (fenced bool, err error) {
	err = s.withWrite(func() error {
		now := time.Now()
		updates := map[string]any{
			"status":      status,
			"verdict":     verdict,
			"report":      report,
			"result_json": resultJSON,
			"fail_reason": failReason,
			"tokens_used": tokensUsed,
			"finished_at": &now,
		}
		if summaryCommentID > 0 {
			updates["summary_comment_id"] = summaryCommentID
		}
		// 任务坐标/attempt 预取（#32 终态差额兜底 + failed 退避共用）：须在更新
		// 前取——failed 路径更新会递增 attempt，补账要对齐本次执行的 attempt_id。
		var cur *Task
		if tokensUsed > 0 || status == StatusFailed {
			var row Task
			if err := s.db.WithContext(ctx).
				Select("platform", "owner", "repo", "attempt").
				Where("id = ?", id).First(&row).Error; err == nil {
				cur = &row
			}
		}
		if status == StatusFailed && cur != nil {
			// failed 退避（v3.8.0）：attempt+1，指数退避（5m×3^n，封顶 1h）后由
			// ResetFailedForRetry 重置重试；成功/stale 路径不动 attempt。
			updates["attempt"] = cur.Attempt + 1
			nr := now.Add(retryBackoff(5*time.Minute, cur.Attempt+1))
			updates["next_retry_at"] = &nr
		}
		// 例外：RunnerID 未装配（单测桩直接 CreateTask(pending)→finish 的合成路径）
		// 时跳过 fencing——生产装配流程 main 必然先赋 RunnerID 再启动 worker。
		q := s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id)
		if s.RunnerID != "" {
			q = q.Where("status = ? AND runner_id = ?", StatusRunning, s.RunnerID)
		}
		res := q.Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		fenced = res.RowsAffected == 0
		if fenced {
			return nil
		}
		// 终态差额兜底（#32）：埋点 best-effort 丢笔时按 tokens_used − 本次执行
		// 已落事件补差——先补账再核销预留（保守序：承诺归还前实销已完整）。
		// 失败只告警不阻断终态写入（缺口由启动期对账 ReconcileUsageEvents 兜不住
		// ——有事件的任务它跳过，此处告警是唯一信号，排查面留痕）。
		if tokensUsed > 0 && cur != nil {
			if berr := s.backfillTaskUsageDelta(ctx, id, cur.Platform, cur.Owner, cur.Repo,
				cur.Attempt, tokensUsed, now); berr != nil {
				s.db.Logger.Warn(ctx, "argus.storage.usage_backfill_failed task=%s err=%v", id, berr)
			}
		}
		// 预留核销（#32 验收③）：任务终态即预留生命周期正常结束——已承诺额度
		// 归还（实销由 usage_event 逐调用在账，不再需要预留代表未来消耗）。
		// fenced（归属易主）不核销：预留属于接管方的执行。失败只告警不阻断：
		// 预留过期兜底仍在（expires_at + 清扫）。
		if rerr := s.releaseTaskReservations(ctx, []string{id}, ReservationSettled, now); rerr != nil {
			s.db.Logger.Warn(ctx, "argus.storage.budget_reserve_settle_failed task=%s err=%v", id, rerr)
		}
		return nil
	})
	return fenced, err
}
