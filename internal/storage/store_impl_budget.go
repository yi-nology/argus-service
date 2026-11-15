// store_impl_budget.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// reserveRepoBudget 的包级实现（转发壳见原文件）。
func reserveRepoBudget(s *Store, ctx context.Context, in ReserveInput) (ReserveResult, error) {
	var out ReserveResult
	if in.QuotaBudget <= 0 && in.RealBudget <= 0 {
		out.GrantedTokens = int64(in.TaskBudget)
		return out, nil // 双账本全关：无额度可超，也无须占承诺
	}
	if in.TaskID == "" || in.Platform == "" {
		return out, errors.New("storage: 预算预留缺少 task_id/仓库坐标")
	}
	ttl := in.TTL
	if ttl == 0 {
		ttl = reservationTTL
	}
	// 短租约串行：check-then-insert 的跨副本互斥（同迁移租约机制，key 按
	// 仓×月切分——同仓同月的启动判定排队，跨仓互不相干）。
	key := reservationLeaseKey(in.Platform, in.Owner, in.Repo, in.Since)
	holder := s.instanceID() + ":reserve:" + in.TaskID
	release, err := s.acquireReservationLease(ctx, key, holder)
	if err != nil {
		return out, err
	}
	defer release()

	now := time.Now()
	// 他任务活跃预留（不含本任务——重试重入时旧行将被刷新，不自我双计）。
	held, err := s.activeHeldTokens(ctx, in.Platform, in.Owner, in.Repo, now, in.TaskID)
	if err != nil {
		return out, err
	}
	// 配额账：done 任务行聚合 + 活跃预留（在途任务终态前不落 done，预留补位）。
	if in.QuotaBudget > 0 {
		done, err := s.RepoMonthlyTokens(ctx, in.Platform, in.Owner, in.Repo, in.Since)
		if err != nil {
			return out, err
		}
		out.QuotaUsed = done + held
		if out.QuotaUsed >= int64(in.QuotaBudget) {
			out.Verdict = ReserveQuotaExceeded
			return out, nil
		}
	}
	// 真实账：usage_event 实销（排除带活跃预留的任务——其潜在消耗已由预留
	// 额度代表）+ 活跃预留。运行中任务的调用即时占用额度。
	if in.RealBudget > 0 {
		settled, err := s.settledNonHeldTokens(ctx, in.Platform, in.Owner, in.Repo, in.Since, now, in.TaskID)
		if err != nil {
			return out, err
		}
		out.RealUsed = settled + held
		if out.RealUsed >= int64(in.RealBudget) {
			out.Verdict = ReserveRealExceeded
			return out, nil
		}
	}
	// 额度判定通过 → 预留额度 = min(任务预算, 各自剩余额度)。
	hold := in.TaskBudget
	if in.QuotaBudget > 0 {
		if r := in.QuotaBudget - int(out.QuotaUsed); r < hold {
			hold = r
		}
	}
	if in.RealBudget > 0 {
		if r := in.RealBudget - int(out.RealUsed); r < hold {
			hold = r
		}
	}
	out.GrantedTokens = int64(hold)
	if hold <= 0 {
		return out, nil // 任务预算非正（无界任务）：不占预留，行为同旧闸直通
	}
	if err := s.upsertReservation(ctx, in, hold, now.Add(ttl), now); err != nil {
		return out, err
	}
	return out, nil
}

// activeHeldTokens 的包级实现（转发壳见原文件）。
func activeHeldTokens(s *Store, ctx context.Context, platform, owner, repo string, now time.Time, excludeTaskID string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND expires_at > ? AND task_id <> ?",
			platform, owner, repo, ReservationReserved, now, excludeTaskID).
		Select("COALESCE(SUM(tokens),0)").Scan(&total).Error
	return total, err
}

// settledNonHeldTokens 的包级实现（转发壳见原文件）。
func settledNonHeldTokens(s *Store, ctx context.Context, platform, owner, repo string,
	since, now time.Time, excludeTaskID string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND occurred_at >= ? "+
			"AND task_id NOT IN (SELECT task_id FROM budget_reservations "+
			"WHERE status = ? AND expires_at > ? AND task_id <> ?)",
			platform, owner, repo, UsageSettled, since,
			ReservationReserved, now, excludeTaskID).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&total).Error
	return total, err
}

// upsertReservation 的包级实现（转发壳见原文件）。
func upsertReservation(s *Store, ctx context.Context, in ReserveInput, tokens int, expires, now time.Time) error {
	updates := map[string]any{
		"attempt_id": in.AttemptID, "platform": in.Platform, "owner": in.Owner,
		"repo": in.Repo, "tokens": tokens, "status": ReservationReserved,
		"expires_at": expires, "updated_at": now,
	}
	res := s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id = ?", in.TaskID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	err := s.quietDB().WithContext(ctx).Create(&BudgetReservation{
		TaskID: in.TaskID, AttemptID: in.AttemptID,
		Platform: in.Platform, Owner: in.Owner, Repo: in.Repo,
		Tokens: tokens, Status: ReservationReserved,
		ExpiresAt: expires, CreatedAt: now, UpdatedAt: now,
	}).Error
	if err == nil {
		return nil
	}
	if !isDuplicateKeyErr(err) {
		return err
	}
	// 撞唯一键：行已存在（并发/重入），回退条件更新收敛。
	res = s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id = ?", in.TaskID).Updates(updates)
	return res.Error
}

// acquireReservationLease 的包级实现（转发壳见原文件）。
func acquireReservationLease(s *Store, ctx context.Context, key, holder string) (func(), error) {
	deadline := time.Now().Add(reservationLeaseWait)
	for {
		ok, err := s.TryAcquire(ctx, key, holder, reservationLeaseTTL)
		if err != nil {
			return nil, fmt.Errorf("storage: 预算预留租约获取失败: %w", err)
		}
		if ok {
			return func() { _ = s.Release(context.Background(), key, holder) }, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("storage: 等待预算预留租约超时（他副本判定中）: %s", key)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// repoMonthlyReserved 的包级实现（转发壳见原文件）。
func repoMonthlyReserved(s *Store, ctx context.Context, platform, owner, repo string) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND expires_at > ?",
			platform, owner, repo, ReservationReserved, time.Now()).
		Select("COALESCE(SUM(tokens),0)").Scan(&total).Error
	return total, err
}

// monthlyReservedByRepo 的包级实现（转发壳见原文件）。
func monthlyReservedByRepo(s *Store, ctx context.Context) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&BudgetReservation{}).
		Select("platform, owner, repo, SUM(tokens) AS tokens, COUNT(DISTINCT task_id) AS tasks").
		Where("status = ? AND expires_at > ?", ReservationReserved, time.Now()).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// releaseExpiredReservations 的包级实现（转发壳见原文件）。
func releaseExpiredReservations(s *Store, ctx context.Context, now time.Time) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).Model(&BudgetReservation{}).
			Where("status = ? AND expires_at < ?", ReservationReserved, now).
			Updates(map[string]any{"status": ReservationReleased, "updated_at": now})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// releaseTaskReservations 的包级实现（转发壳见原文件）。
func releaseTaskReservations(s *Store, ctx context.Context, taskIDs []string, to string, now time.Time) error {
	if len(taskIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id IN ? AND status = ?", taskIDs, ReservationReserved).
		Updates(map[string]any{"status": to, "updated_at": now}).Error
}

// renewTaskReservations 的包级实现（转发壳见原文件）。
func renewTaskReservations(s *Store, ctx context.Context, taskIDs []string, now time.Time) error {
	if len(taskIDs) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&BudgetReservation{}).
		Where("task_id IN ? AND status = ?", taskIDs, ReservationReserved).
		Updates(map[string]any{
			"expires_at": now.Add(reservationTTL), "updated_at": now,
		}).Error
}
