// store_task.go —— tasks 表生命周期：崩溃复位/心跳续期/抢占/重试与状态写回。

package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ResetRunningToPending 把心跳过期的 running 任务重置回 pending
// （§10.4：崩溃任务否则静默丢失）。staleAfter 取心跳间隔的 ≥3 倍：
// 只有 heartbeat_at 已死（或遗留的 NULL 心跳旧行）才重置——多副本共库时，
// 一个实例滚动重启不会把其他存活实例正在执行的任务也重置（那会双执行、双回帖）。
// #32 崩溃恢复：被复位任务的未核销预留就地释放（心跳已死 = 持有进程不会
// 再续租，不等租约过期）——逐 id 条件更新（心跳在挑选与复位之间复活的任务
// 不被复位、也不误释放预留），复位成功才释放。
func (s *Store) ResetRunningToPending(ctx context.Context, staleAfter time.Duration) (n int64, err error) {
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

// TouchRunningHeartbeats 续本实例全部 running 任务的心跳（worker 池周期调用）。
// 预留租约随心跳同步续期（#32：expires_at 与任务活性绑定）。
func (s *Store) TouchRunningHeartbeats(ctx context.Context) error {
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

// TouchHeartbeatsFor 只续期 taskIDs 中属于本实例且在跑的任务心跳
// （oagentkit/queue worker.ScopedHeartbeater 实现：Pool 探测到本能力后按本进程
// 在飞任务集续期，替代盲续 TouchRunningHeartbeats）。
//
// 三重守卫缺一不可（oagentkit/queue#42）：runner_id 防续他实例的行；
// status='running' 防把已终态的行续回新鲜；id IN (...) 是 scoped 语义的实质
// ——不在续期集里的行（runTask 已 panic/报错/成功退出的）心跳就此冻结，
// staleAfter 后由 ResetRunningToPending 复位重跑。盲续路径下这些行被本实例
// 一直续活，复位永不命中（僵尸任务静默丢失的根因）。
func (s *Store) TouchHeartbeatsFor(ctx context.Context, taskIDs []string) error {
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

// ClaimNextPending worker 原子抢占一个 pending 任务（pending→running）。
// 返回 ok=false 表示当前无任务。
// MySQL 走 FOR UPDATE SKIP LOCKED 单语句取+占（多副本无空转）；SQLite 保持
// 两步条件更新（单写模型下无竞争）。抢锁失败少量重试而非直接报"无任务"——
// 此前败者直接 ok=false，worker 睡 500ms，队列有任务也空转（深挖修复）。
func (s *Store) ClaimNextPending(ctx context.Context) (*Task, bool, error) {
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

// ResetFailedForRetry failed 任务的到期重试（v3.8.0）：failed 且 attempt < maxRetries
// 且退避到期的任务重置回 pending（attempt 保留，防循环）。maxRetries<=0 = 禁用。
// 重启不重置 failed——只有此入口（或 TaskAPI 手工重审）能复活失败任务。
func (s *Store) ResetFailedForRetry(ctx context.Context, maxRetries int, now time.Time) (n int64, err error) {
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
