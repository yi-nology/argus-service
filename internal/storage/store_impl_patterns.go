// store_impl_patterns.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/tenant"
	"gorm.io/gorm"
)

// bumpPattern 的包级实现（转发壳见原文件）。
func bumpPattern(s *Store, ctx context.Context, platform, owner, repo, pluginName string,
	f domain.Finding, column string, at time.Time) error {
	if platform == "" || owner == "" || repo == "" {
		return nil
	}
	if pluginName == "" {
		pluginName = "unknown"
	}
	key := learning.PatternKey(pluginName, f.Comment)
	text := learning.PatternSample(f.Comment)

	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var row PatternSignal
			err := tx.Where("platform = ? AND owner = ? AND repo = ? AND plugin = ? AND pattern_key = ?",
				platform, owner, repo, pluginName, key).First(&row).Error
			if err == gorm.ErrRecordNotFound {
				row = PatternSignal{
					Platform: platform, Owner: owner, Repo: repo,
					Plugin: pluginName, Pattern: key, PatternText: text,
					FirstSeenAt: at, LastSeenAt: at,
				}
				switch column {
				case "posted":
					row.Posted = 1
				case "regret":
					row.Regret = 1
				case "suppress":
					row.Suppress = 1
				case "restore":
					row.Restore = 1
				default:
					return nil
				}
				return tx.Create(&row).Error
			}
			if err != nil {
				return err
			}
			if text != "" && row.PatternText == "" {
				row.PatternText = text
			}
			row.LastSeenAt = at
			return tx.Model(&row).Updates(map[string]any{
				column:       gorm.Expr(column + " + 1"),
				"updated_at": at,
				"pattern_text": func() string {
					if row.PatternText != "" {
						return row.PatternText
					}
					return text
				}(),
				"last_seen_at": at,
			}).Error
		})
	})
}

// patternPosted 的包级实现（转发壳见原文件）。
func patternPosted(s *Store, ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "posted", at)
}

// patternRegret 的包级实现（转发壳见原文件）。
func patternRegret(s *Store, ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "regret", at)
}

// patternSuppress 的包级实现（转发壳见原文件）。
func patternSuppress(s *Store, ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "suppress", at)
}

// patternRestore 的包级实现（转发壳见原文件）。
func patternRestore(s *Store, ctx context.Context, platform, owner, repo, pluginName string, f domain.Finding, at time.Time) error {
	return s.bumpPattern(ctx, platform, owner, repo, pluginName, f, "restore", at)
}

// listLowConfidencePatterns 的包级实现（转发壳见原文件）。
func listLowConfidencePatterns(s *Store, ctx context.Context, platform, owner, repo string,
	minSamples int, maxConfidence float64, limit int) ([]learning.PatternState, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND (posted + regret + suppress) >= ?",
			platform, owner, repo, minSamples).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]learning.PatternState, 0, len(rows))
	for i := range rows {
		p := rows[i].project()
		if p.Confidence >= maxConfidence {
			continue
		}
		out = append(out, p)
	}
	// 确定性排序：置信度升序，同分按 pattern
	slices.SortFunc(out, func(a, b learning.PatternState) int {
		if c := cmp.Compare(a.Confidence, b.Confidence); c != 0 {
			return c
		}
		return strings.Compare(a.Pattern, b.Pattern)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// getPattern 的包级实现（转发壳见原文件）。
func getPattern(s *Store, ctx context.Context, platform, owner, repo, pluginName, comment string) (*learning.PatternState, error) {
	key := learning.PatternKey(pluginName, comment)
	var row PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND plugin = ? AND pattern_key = ?",
			platform, owner, repo, pluginName, key).First(&row).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := row.project()
	return &p, nil
}

// listRepoPatterns 的包级实现（转发壳见原文件）。
func listRepoPatterns(s *Store, ctx context.Context, platform, owner, repo string) ([]PatternSignal, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo).
		Order("id").Find(&rows).Error
	return rows, err
}

// listPatternRepos 的包级实现（转发壳见原文件）。
// dream 是跨租户全局聚合器（#84）：走 WithoutScope 逃生门列举全部租户坐标，
// 租户段随行带回——逐仓处理时用行租户构造 ctx，存储谓词自然按租户收口。
func listPatternRepos(s *Store, ctx context.Context) ([]learning.RepoRef, error) {
	type row struct {
		TenantID string
		Platform string
		Owner    string
		Repo     string
	}
	var rows []row
	err := s.db.WithContext(tenant.WithoutScope(ctx)).Model(&PatternSignal{}).
		Distinct("tenant_id", "platform", "owner", "repo").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]learning.RepoRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, learning.RepoRef{Platform: r.Platform, Owner: r.Owner,
			Repo: r.Repo, Tenant: tenant.Normalize(r.TenantID)})
	}
	return out, nil
}

// mergePatternInto 的包级实现（转发壳见原文件）。
func mergePatternInto(s *Store, ctx context.Context, platform, owner, repo string, keepID, dupID uint, at time.Time) error {
	if keepID == 0 || dupID == 0 || keepID == dupID {
		return nil
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var keep, dup PatternSignal
			if err := tx.Where("id = ? AND platform = ? AND owner = ? AND repo = ?",
				keepID, platform, owner, repo).First(&keep).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ? AND platform = ? AND owner = ? AND repo = ?",
				dupID, platform, owner, repo).First(&dup).Error; err != nil {
				return err
			}
			if err := tx.Model(&keep).Updates(map[string]any{
				"posted":        gorm.Expr("posted + ?", dup.Posted),
				"regret":        gorm.Expr("regret + ?", dup.Regret),
				"suppress":      gorm.Expr("suppress + ?", dup.Suppress),
				"restore":       gorm.Expr("restore + ?", dup.Restore),
				"last_seen_at":  laterTime(keep.LastSeenAt, dup.LastSeenAt),
				"first_seen_at": earlierTime(keep.FirstSeenAt, dup.FirstSeenAt),
				"updated_at":    at,
			}).Error; err != nil {
				return err
			}
			return tx.Delete(&dup).Error
		})
	})
}

// pruneStalePatterns 的包级实现（转发壳见原文件）。
func pruneStalePatterns(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []uint
			if err := s.db.WithContext(ctx).Model(&PatternSignal{}).
				Where("last_seen_at < ?", cutoff).Limit(500).Pluck("id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("id IN ?", batch).Delete(&PatternSignal{})
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

// listAllPatternSignals 的包级实现（转发壳见原文件）。
func listAllPatternSignals(s *Store, ctx context.Context) ([]PatternSignal, error) {
	var rows []PatternSignal
	err := s.db.WithContext(ctx).Order("id").Find(&rows).Error
	return rows, err
}

// listRepoPatternStates 的包级实现（转发壳见原文件）。
func listRepoPatternStates(s *Store, ctx context.Context, platform, owner, repo string) ([]learning.PatternState, error) {
	rows, err := s.ListRepoPatterns(ctx, platform, owner, repo)
	if err != nil {
		return nil, err
	}
	out := make([]learning.PatternState, 0, len(rows))
	for _, p := range rows {
		out = append(out, p.project())
	}
	return out, nil
}
