// store_impl_task_lifecycle.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/yi-nology/argus/internal/tenant"
)

// resetRunningToPending 的包级实现（转发壳见原文件）。
// 调度面操作（#88）：worker 池服务全部租户——谓词注入会让非 default 租户任务
// 心跳过期被误复位双跑；runner_id 归属已是隔离边界，此处显式 WithoutScope。
func resetRunningToPending(s *Store, ctx context.Context, staleAfter time.Duration) (n int64, err error) {
	ctx = tenant.WithoutScope(ctx)
	err = s.withWrite(func() error {
		now := time.Now()
		cutoff := now.Add(-staleAfter)
		var ids []string
		if err := s.db.WithContext(ctx).Model(&Task{}).
			Where("status = ? AND (heartbeat_at IS NULL OR heartbeat_at < ?)", StatusRunning, cutoff).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		var reset []string
		for _, id := range ids {
			// 逐 id 复位：条件在本语句内原子复核（与旧单语句同语义），
			// RowsAffected=1 才算真复位——复位与释放一一对应。
			res := s.db.WithContext(ctx).Model(&Task{}).
				Where("id = ? AND status = ? AND (heartbeat_at IS NULL OR heartbeat_at < ?)",
					id, StatusRunning, cutoff).
				Update("status", StatusPending)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected > 0 {
				reset = append(reset, id)
			}
		}
		n = int64(len(reset))
		return s.releaseTaskReservations(ctx, reset, ReservationReleased, now)
	})
	return n, err
}

// touchRunningHeartbeats 的包级实现（转发壳见原文件）。调度面（#88）：WithoutScope。
func touchRunningHeartbeats(s *Store, ctx context.Context) error {
	ctx = tenant.WithoutScope(ctx)
	if s.RunnerID == "" {
		// 显式失败拒绝执行：空 RunnerID 的续期无法归属实例，静默 no-op 会让
		// 本实例任务被 reaper 反复重置重跑（装配纪律：main 先赋 RunnerID 再启动 worker）。
		return errors.New("storage: RunnerID 未装配，心跳续期拒绝执行")
	}
	return s.withWrite(func() error {
		now := time.Now()
		var ids []string
		if err := s.db.WithContext(ctx).Model(&Task{}).
			Where("status = ? AND runner_id = ?", StatusRunning, s.RunnerID).
			Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := s.db.WithContext(ctx).Model(&Task{}).
			Where("id IN ? AND status = ? AND runner_id = ?", ids, StatusRunning, s.RunnerID).
			Update("heartbeat_at", now).Error; err != nil {
			return err
		}
		return s.renewTaskReservations(ctx, ids, now)
	})
}

// touchHeartbeatsFor 的包级实现（转发壳见原文件）。调度面（#88）：WithoutScope。
func touchHeartbeatsFor(s *Store, ctx context.Context, taskIDs []string) error {
	ctx = tenant.WithoutScope(ctx)
	if s.RunnerID == "" {
		// 装配纪律同 TouchRunningHeartbeats：空 RunnerID 的续期无法归属实例
		return errors.New("storage: RunnerID 未装配，心跳续期拒绝执行")
	}
	if len(taskIDs) == 0 {
		// 空集绝不能退化成「无过滤条件」——那是盲续，正是要修的东西
		// （Pool 空集时根本不调用，此处自守一层防 SQL 拼装错误）
		return nil
	}
	return s.withWrite(func() error {
		now := time.Now()
		if err := s.db.WithContext(ctx).Model(&Task{}).
			Where("status = ? AND runner_id = ? AND id IN ?", StatusRunning, s.RunnerID, taskIDs).
			Update("heartbeat_at", now).Error; err != nil {
			return err
		}
		// 预留租约随心跳续期（#32 验收④）：expires_at 与任务活性绑定——
		// 进程活着预留不掉，进程死心跳停 → 预留到期释放（清扫+查询双兜底）。
		return s.renewTaskReservations(ctx, taskIDs, now)
	})
}

// claimNextPending 的包级实现（转发壳见原文件）。调度面（#88）：WithoutScope
// ——认领是跨租户队列分发，谓词注入会让非 default 租户任务永不被认领。
func claimNextPending(s *Store, ctx context.Context) (*Task, bool, error) {
	ctx = tenant.WithoutScope(ctx)
	var claimed *Task
	err := s.withWrite(func() error {
		const maxAttempts = 3
		return s.quietDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for range maxAttempts {
				var t Task
				// 优先级队列（v3.40.0）：人类在等的任务（评论重审/API 手工）先于
				// poller 批量；同优先级保持 FIFO
				q := tx.Where("status = ?", StatusPending).Order("priority DESC, created_at ASC, id ASC")
				if !s.isSQLite() {
					q = q.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"})
				}
				err := q.First(&t).Error
				if err != nil {
					return err // gorm.ErrRecordNotFound → 无任务
				}
				now := time.Now()
				res := tx.Model(&Task{}).Where("id = ? AND status = ?", t.ID, StatusPending).
					Updates(map[string]any{"status": StatusRunning, "started_at": &now,
						"runner_id": s.RunnerID, "heartbeat_at": now})
				if res.Error != nil {
					return res.Error
				}
				if res.RowsAffected == 0 {
					continue // 被并发抢走：重试取下一条（SKIP LOCKED 下基本不发生）
				}
				t.Status = StatusRunning
				t.StartedAt = &now
				t.RunnerID = s.RunnerID
				t.HeartbeatAt = &now
				claimed = &t
				return nil
			}
			return gorm.ErrRecordNotFound
		})
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return claimed, claimed != nil, nil
}

// resetFailedForRetry 的包级实现（转发壳见原文件）。调度面（#88）：WithoutScope。
func resetFailedForRetry(s *Store, ctx context.Context, maxRetries int, now time.Time) (n int64, err error) {
	ctx = tenant.WithoutScope(ctx)
	if maxRetries <= 0 {
		return 0, nil
	}
	err = s.withWrite(func() error {
		res := s.db.WithContext(ctx).Model(&Task{}).
			Where("status = ? AND attempt < ? AND next_retry_at IS NOT NULL AND next_retry_at <= ?",
				StatusFailed, maxRetries, now).
			Update("status", StatusPending)
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// finishTask 的包级实现（转发壳见原文件）。
func finishTask(s *Store, ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
	tokensUsed int, summaryCommentID int64) error {
	_, err := s.FinishTaskFenced(ctx, id, status, verdict, report, resultJSON, failReason, tokensUsed, summaryCommentID)
	return err
}

// finishTaskFenced 的包级实现（转发壳见原文件）。
func finishTaskFenced(s *Store, ctx context.Context, id, status, verdict, report, resultJSON, failReason string,
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

// updateTaskFixStatus 的包级实现（转发壳见原文件）。
func updateTaskFixStatus(s *Store, ctx context.Context, id, fixStatus string) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&Task{}).
			Where("id = ?", id).Update("fix_status", fixStatus).Error
	})
}

// setTaskHeadSHA 的包级实现（转发壳见原文件）。
func setTaskHeadSHA(s *Store, ctx context.Context, id, sha string) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("head_sha", sha).Error
	})
}

// setTaskDebug 的包级实现（转发壳见原文件）。
func setTaskDebug(s *Store, ctx context.Context, id, debugJSON string) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("debug_json", debugJSON).Error
	})
}

// markReviewPosted 的包级实现（转发壳见原文件）。
func markReviewPosted(s *Store, ctx context.Context, id string) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("review_posted", true).Error
	})
}
