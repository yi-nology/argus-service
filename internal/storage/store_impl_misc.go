// store_impl_misc.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"errors"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"gorm.io/gorm"
)

// upsertFPCase 的包级实现（转发壳见原文件）。
func upsertFPCase(s *Store, ctx context.Context, bucket FPCaseBucket) error {
	if bucket.Pattern == "" || bucket.Org == "" {
		return errors.New("fpcase: Pattern/Org 必填")
	}
	if bucket.Status == "" {
		bucket.Status = domain.FPCaseCandidate
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var existing FPCaseBucket
			err := tx.Where("pattern = ?", bucket.Pattern).First(&existing).Error
			if err == nil {
				return tx.Model(&FPCaseBucket{}).Where("id = ?", existing.ID).
					UpdateColumn("hit_count", gorm.Expr("hit_count + 1")).Error
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return tx.Create(&bucket).Error
		})
	})
}

// listFPCaseCandidates 的包级实现（转发壳见原文件）。
func listFPCaseCandidates(s *Store, ctx context.Context, limit int) ([]FPCaseBucket, error) {
	q := s.db.WithContext(ctx).Where("status = ?", domain.FPCaseCandidate).
		Order("hit_count DESC, updated_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []FPCaseBucket
	err := q.Find(&out).Error
	return out, err
}

// confirmFPCase 的包级实现（转发壳见原文件）。
func confirmFPCase(s *Store, ctx context.Context, pattern, status string) error {
	if status != domain.FPCaseConfirmed && status != domain.FPCaseRejected {
		return errors.New("fpcase: status 须 confirmed|rejected")
	}
	return s.withWrite(func() error {
		res := s.db.WithContext(ctx).Model(&FPCaseBucket{}).
			Where("pattern = ? AND status = ?", pattern, domain.FPCaseCandidate).
			Update("status", status)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

// matchFPCase 的包级实现（转发壳见原文件）。
func matchFPCase(s *Store, ctx context.Context, pattern string) (*FPCaseBucket, error) {
	var b FPCaseBucket
	err := s.db.WithContext(ctx).
		Where("pattern = ? AND status = ?", pattern, domain.FPCaseConfirmed).
		First(&b).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

// latestDoneTask 的包级实现（转发壳见原文件）。
func latestDoneTask(s *Store, ctx context.Context, platform, owner, repo, number string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ?",
			platform, owner, repo, number, StatusDone).
		Order("finished_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// getQASession 的包级实现（转发壳见原文件）。
func getQASession(s *Store, ctx context.Context, sessionKey string, ttl time.Duration) (*QASession, error) {
	var row QASession
	err := s.db.WithContext(ctx).Where("session_key = ?", sessionKey).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if time.Since(row.UpdatedAt) > ttl {
		return nil, nil // 过期：新话题，历史清零
	}
	return &row, nil
}

// saveQASession 的包级实现（转发壳见原文件）。
func saveQASession(s *Store, ctx context.Context, sess *QASession) error {
	sess.UpdatedAt = time.Now().UTC()
	return s.db.WithContext(ctx).Save(sess).Error
}

// saveQASessionCas 的包级实现（转发壳见原文件）。
func saveQASessionCas(s *Store, ctx context.Context, sess *QASession, expect time.Time, check bool) (bool, error) {
	sess.UpdatedAt = time.Now().UTC()
	tx := s.db.WithContext(ctx).Model(&QASession{}).
		Where("session_key = ?", sess.SessionKey)
	if check {
		tx = tx.Where("updated_at = ?", expect)
	}
	res := tx.Updates(map[string]any{
		"history_json": sess.HistoryJSON,
		"summary":      sess.Summary,
		"updated_at":   sess.UpdatedAt,
	})
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected == 1 {
		return true, nil
	}
	if !check {
		// 无条件写但行不存在 → 插入
		return true, s.db.WithContext(ctx).Create(sess).Error
	}
	return false, nil
}
