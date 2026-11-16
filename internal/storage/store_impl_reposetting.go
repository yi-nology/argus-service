// store_impl_reposetting.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/oekit/data/gormx"
	"gorm.io/gorm"
)

// getRepoSetting 的包级实现（转发壳见原文件）。
func getRepoSetting(s *Store, ctx context.Context, platform, owner, repo string) (*RepoSetting, error) {
	if platform == "" || owner == "" || repo == "" {
		return nil, errors.New("storage: GetRepoSetting 缺 platform/owner/repo")
	}
	var row RepoSetting
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ?", platform, owner, repo).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// upsertRepoSetting 的包级实现（转发壳见原文件）。
func upsertRepoSetting(s *Store, ctx context.Context, row *RepoSetting) error {
	if row == nil || row.Platform == "" || row.Owner == "" || row.Repo == "" {
		return errors.New("storage: UpsertRepoSetting 缺主键")
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var cur RepoSetting
			err := tx.Where("platform = ? AND owner = ? AND repo = ?",
				row.Platform, row.Owner, row.Repo).First(&cur).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return tx.Create(row).Error
			}
			if err != nil {
				return err
			}
			return tx.Model(&cur).Updates(map[string]any{
				"settings_json": row.SettingsJSON,
				"updated_by":    row.UpdatedBy,
				"updated_at":    row.UpdatedAt,
			}).Error
		})
	})
}

// deleteRepoSetting 的包级实现（转发壳见原文件）。
func deleteRepoSetting(s *Store, ctx context.Context, platform, owner, repo string) (bool, error) {
	if platform == "" || owner == "" || repo == "" {
		return false, errors.New("storage: DeleteRepoSetting 缺 platform/owner/repo")
	}
	var deleted bool
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).Where(
			"platform = ? AND owner = ? AND repo = ?", platform, owner, repo).Delete(&RepoSetting{})
		deleted = res.RowsAffected > 0
		return res.Error
	})
	return deleted, err
}

// listRepoSettings 的包级实现（转发壳见原文件）。
func listRepoSettings(s *Store, ctx context.Context) ([]*RepoSetting, error) {
	var rows []*RepoSetting
	err := s.db.WithContext(ctx).
		Order("platform ASC, owner ASC, repo ASC").Find(&rows).Error
	return rows, err
}

// clearReviewTargetComment 的包级实现（转发壳见原文件）。
func clearReviewTargetComment(s *Store, ctx context.Context, platform, owner, repo, number string) error {
	return s.withWrite(func() error {
		return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
			return wherePR(tx.Model(&ReviewTarget{}), platform, owner, repo, number).
				Update("last_summary_comment_id", 0).Error
		})
	})
}

// appendReviewTargetComment 的包级实现（转发壳见原文件）。
func appendReviewTargetComment(s *Store, ctx context.Context, platform, owner, repo, number string, id int64) error {
	if id <= 0 {
		return nil
	}
	return s.withWrite(func() error {
		return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
			var t ReviewTarget
			err := wherePR(tx, platform, owner, repo, number).First(&t).Error
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				t = ReviewTarget{Platform: platform, Owner: owner, Repo: repo, Number: number}
			}
			var ids []int64
			if t.KnownCommentIDs != "" {
				_ = json.Unmarshal([]byte(t.KnownCommentIDs), &ids)
			}
			for _, e := range ids {
				if e == id {
					return nil
				}
			}
			ids = append(ids, id)
			if len(ids) > knownCommentIDsCap {
				ids = ids[len(ids)-knownCommentIDsCap:]
			}
			b, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			t.KnownCommentIDs = string(b)
			return tx.Save(&t).Error
		})
	})
}

// knownReviewTargetComments 的包级实现（转发壳见原文件）。
func knownReviewTargetComments(s *Store, ctx context.Context, platform, owner, repo, number string) ([]int64, error) {
	var t ReviewTarget
	err := wherePR(s.db.WithContext(ctx), platform, owner, repo, number).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if t.KnownCommentIDs == "" {
		return nil, nil
	}
	var ids []int64
	if err := json.Unmarshal([]byte(t.KnownCommentIDs), &ids); err != nil {
		return nil, nil // 缓存损坏按无缓存处理，不阻断回帖
	}
	// 新→旧：倒序遍历（最新评论最可能仍有效）
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids, nil
}

// getReviewTarget 的包级实现（转发壳见原文件）。
func getReviewTarget(s *Store, ctx context.Context, platform, owner, repo, number string) (*ReviewTarget, error) {
	var t ReviewTarget
	err := wherePR(s.quietDB().WithContext(ctx), platform, owner, repo, number).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// upsertReviewTarget 的包级实现（转发壳见原文件）。
func upsertReviewTarget(s *Store, ctx context.Context, platform, owner, repo, number string,
	summaryCommentID int64, headSHA string) error {
	return s.withWrite(func() error {
		return gormx.InTransactionContext(ctx, s.db, func(tx *gorm.DB) error {
			var t ReviewTarget
			err := wherePR(tx, platform, owner, repo, number).First(&t).Error
			if err != nil {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				t = ReviewTarget{Platform: platform, Owner: owner, Repo: repo, Number: number}
			}
			if summaryCommentID > 0 {
				t.LastSummaryCommentID = summaryCommentID
			}
			if headSHA != "" {
				t.LastHeadSHA = headSHA
			}
			return tx.Save(&t).Error
		})
	})
}
