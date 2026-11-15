// store_impl_prune.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"time"
)

// pruneTasks 的包级实现（转发壳见原文件）。
func pruneTasks(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&Task{}).
				Where("status IN ? AND created_at < ?", []string{StatusDone, StatusFailed, StatusStale}, cutoff).
				Limit(500).Pluck("id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("id IN ?", batch).Delete(&Task{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// pruneQASessions 的包级实现（转发壳见原文件）。
func pruneQASessions(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&QASession{}).
				Where("updated_at < ?", cutoff).Limit(500).Pluck("session_key", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("session_key IN ?", batch).Delete(&QASession{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// pruneStalePrints 的包级实现（转发壳见原文件）。
func pruneStalePrints(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&FindingPrint{}).
				Where("last_seen_at < ?", cutoff).Limit(500).Pluck("print", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("print IN ?", batch).Delete(&FindingPrint{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if err := s.db.WithContext(ctx).
				Where("print_fp IN ?", batch).Delete(&PrintAlias{}).Error; err != nil {
				return err
			}
			if len(batch) < 500 {
				return nil
			}
		}
	})
	return total, err
}

// repoMonthlyTokens 的包级实现（转发壳见原文件）。
func repoMonthlyTokens(s *Store, ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND created_at >= ?",
			platform, owner, repo, StatusDone, since).
		Select("COALESCE(SUM(tokens_used),0)").Scan(&total).Error
	return total, err
}

// monthlyTokensByRepo 的包级实现（转发壳见原文件）。
func monthlyTokensByRepo(s *Store, ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&Task{}).
		Select("platform, owner, repo, SUM(tokens_used) AS tokens, COUNT(*) AS tasks").
		Where("status = ? AND created_at >= ?", StatusDone, since).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// repoMonthlyTokensReal 的包级实现（转发壳见原文件）。
func repoMonthlyTokensReal(s *Store, ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	var total int64
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("platform = ? AND owner = ? AND repo = ? AND status = ? AND occurred_at >= ?",
			platform, owner, repo, UsageSettled, since).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&total).Error
	return total, err
}

// monthlyTokensByRepoReal 的包级实现（转发壳见原文件）。
func monthlyTokensByRepoReal(s *Store, ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	var out []RepoTokenUsage
	err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Select("platform, owner, repo, SUM(input_tokens + output_tokens) AS tokens, "+
			"COUNT(DISTINCT task_id) AS tasks").
		Where("status = ? AND occurred_at >= ?", UsageSettled, since).
		Group("platform, owner, repo").Order("tokens DESC").Find(&out).Error
	return out, err
}

// pruneUsageEvents 的包级实现（转发壳见原文件）。
func pruneUsageEvents(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var ids []uint
			if err := s.db.WithContext(ctx).Model(&UsageEvent{}).
				Where("occurred_at < ?", cutoff).Limit(500).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&UsageEvent{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(ids) < 500 {
				break
			}
		}
		for {
			var ids []uint
			if err := s.db.WithContext(ctx).Model(&BudgetReservation{}).
				Where("status <> ? AND updated_at < ?", ReservationReserved, cutoff).
				Limit(500).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				break
			}
			res := s.db.WithContext(ctx).Where("id IN ?", ids).Delete(&BudgetReservation{})
			total += res.RowsAffected
			if res.Error != nil {
				return res.Error
			}
			if len(ids) < 500 {
				break
			}
		}
		return nil
	})
	return total, err
}

// statsOverview 的包级实现（转发壳见原文件）。
func statsOverview(s *Store, ctx context.Context, since time.Time, platform, owner, repo string) ([]RepoStats, error) {
	var aggs []struct {
		Platform  string
		Owner     string
		Repo      string
		TasksDone int64
		Approved  int64
		Commented int64
		Rejected  int64
		Tokens    int64
	}
	tq := s.quietDB().WithContext(ctx).Model(&Task{}).
		Select("platform, owner, repo, COUNT(*) AS tasks_done, "+
			"COALESCE(SUM(CASE WHEN verdict = 'approve' THEN 1 ELSE 0 END),0) AS approved, "+
			"COALESCE(SUM(CASE WHEN verdict = 'comment' THEN 1 ELSE 0 END),0) AS commented, "+
			"COALESCE(SUM(CASE WHEN verdict = 'request_changes' THEN 1 ELSE 0 END),0) AS rejected, "+
			"COALESCE(SUM(tokens_used),0) AS tokens").
		Where("status = ? AND created_at >= ?", StatusDone, since).
		Group("platform, owner, repo")
	if platform != "" {
		tq = tq.Where("platform = ?", platform)
	}
	if owner != "" {
		tq = tq.Where("owner = ?", owner)
	}
	if repo != "" {
		tq = tq.Where("repo = ?", repo)
	}
	if err := tq.Order("tasks_done DESC").Find(&aggs).Error; err != nil {
		return nil, err
	}

	var sups []struct {
		Platform string
		Owner    string
		Repo     string
		N        int64
	}
	sq := s.quietDB().WithContext(ctx).Model(&FindingPrint{}).
		Select("platform, owner, repo, COUNT(*) AS n").
		Where("status = ? AND first_seen_at >= ?", PrintSuppressed, since).
		Group("platform, owner, repo")
	if platform != "" {
		sq = sq.Where("platform = ?", platform)
	}
	if owner != "" {
		sq = sq.Where("owner = ?", owner)
	}
	if repo != "" {
		sq = sq.Where("repo = ?", repo)
	}
	if err := sq.Find(&sups).Error; err != nil {
		return nil, err
	}

	supMap := make(map[string]int64, len(sups))
	for _, x := range sups {
		supMap[x.Platform+"/"+x.Owner+"/"+x.Repo] = x.N
	}
	out := make([]RepoStats, 0, len(aggs))
	for _, a := range aggs {
		row := RepoStats{
			Platform: a.Platform, Owner: a.Owner, Repo: a.Repo,
			TasksDone: a.TasksDone, Approved: a.Approved,
			Commented: a.Commented, Rejected: a.Rejected, Tokens: a.Tokens,
			Suppressed: supMap[a.Platform+"/"+a.Owner+"/"+a.Repo],
		}
		out = append(out, row)
	}
	return out, nil
}
