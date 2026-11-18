// store_impl_outbox.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/tenant"
)

// markCommentDelivery 的包级实现（转发壳见原文件）。
func markCommentDelivery(s *Store, ctx context.Context, deliveryID string, at time.Time) (fresh bool, err error) {
	if deliveryID == "" {
		return true, nil
	}
	err = s.withWrite(func() error {
		derr := s.db.WithContext(ctx).Create(&CommentDelivery{DeliveryID: deliveryID, SeenAt: at}).Error
		if derr != nil {
			if isDuplicateKeyErr(derr) {
				return nil
			}
			return derr
		}
		fresh = true
		return nil
	})
	return fresh, err
}

// markCommentDeliveryOutcome 的包级实现（转发壳见原文件）。
func markCommentDeliveryOutcome(s *Store, ctx context.Context, deliveryID, status string, commentID int64, ref string) error {
	if deliveryID == "" {
		return nil
	}
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Model(&CommentDelivery{}).
			Where("delivery_id = ?", deliveryID).
			Updates(map[string]any{"reply_status": status, "comment_id": commentID, "ref": ref}).Error
	})
}

// outOfSyncDeliveries 的包级实现（转发壳见原文件）。
func outOfSyncDeliveries(s *Store, ctx context.Context, since time.Time) (postedButFailed, doneWithoutReply int64, err error) {
	if e := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ? AND summary_comment_id > 0 AND finished_at >= ?", StatusFailed, since).
		Count(&postedButFailed).Error; e != nil {
		return 0, 0, e
	}
	if e := s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ? AND (summary_comment_id IS NULL OR summary_comment_id = 0) AND finished_at >= ? AND kind IN ?",
			StatusDone, since, []string{"requirement_compliance", "code_review", "visual_check"}).
		Count(&doneWithoutReply).Error; e != nil {
		return 0, 0, e
	}
	return postedButFailed, doneWithoutReply, nil
}

// pruneCommentDeliveries 的包级实现（转发壳见原文件）。
func pruneCommentDeliveries(s *Store, ctx context.Context, cutoff time.Time) (total int64, err error) {
	err = s.withWrite(func() error {
		for {
			var batch []string
			if err := s.db.WithContext(ctx).Model(&CommentDelivery{}).
				Where("seen_at < ?", cutoff).Limit(500).Pluck("delivery_id", &batch).Error; err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			res := s.db.WithContext(ctx).Where("delivery_id IN ?", batch).Delete(&CommentDelivery{})
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

// appendOutboxEvent 的包级实现（转发壳见原文件）。
func appendOutboxEvent(s *Store, ctx context.Context, ev progress.Event) error {
	// #109：归属列带事件租户（Event.Tenant #105 起流转；旧事件空 = default
	// 回填语义与全库一致）。
	row := OutboxEvent{
		Name: ev.ID, TenantID: tenant.Normalize(ev.Tenant),
		Platform: ev.Platform, Owner: ev.Owner, Repo: ev.Repo,
		Type: ev.Type, CreatedAt: time.Now(),
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	row.Body = string(b)
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Create(&row).Error
	})
}

// listOutboxEvents 的包级实现（转发壳见原文件）。
func listOutboxEvents(s *Store, ctx context.Context, afterID int64, limit int) ([]OutboxRow, error) {
	if limit <= 0 {
		limit = 200
	}
	var rows []OutboxEvent
	err := s.quietDB().WithContext(ctx).
		Where("id > ?", afterID).Order("id ASC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]OutboxRow, 0, len(rows))
	for _, r := range rows {
		var ev progress.Event
		if err := jsonUnmarshal([]byte(r.Body), &ev); err != nil {
			continue // 坏行跳过（fanout 不断流）
		}
		out = append(out, OutboxRow{ID: r.ID, Event: ev})
	}
	return out, nil
}

// maxOutboxID 的包级实现（转发壳见原文件）。
func maxOutboxID(s *Store, ctx context.Context) (int64, error) {
	var maxID *int64
	err := s.quietDB().WithContext(ctx).Model(&OutboxEvent{}).Select("MAX(id)").Scan(&maxID).Error
	if err != nil || maxID == nil {
		return 0, err
	}
	return *maxID, nil
}

// pruneOutboxEvents 的包级实现（转发壳见原文件）。
func pruneOutboxEvents(s *Store, ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).Where("created_at < ?", cutoff).Delete(&OutboxEvent{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}

// pruneDeltaOutboxEvents 的包级实现（转发壳见原文件）：只清 agent.delta 类型行
// （"agent.delta" 字面量与 progress.EventAgentDelta 同值——storage 已 import
// progress，直接用常量）。
func pruneDeltaOutboxEvents(s *Store, ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.withWrite(func() error {
		res := s.db.WithContext(ctx).
			Where("created_at < ? AND type = ?", cutoff, progress.EventAgentDelta).
			Delete(&OutboxEvent{})
		n = res.RowsAffected
		return res.Error
	})
	return n, err
}
