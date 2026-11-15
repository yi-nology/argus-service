// store_impl_usage.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// recordUsageEvent 的包级实现（转发壳见原文件）。
func recordUsageEvent(s *Store, ctx context.Context, ev *UsageEvent) error {
	if ev == nil {
		return errors.New("storage: usage_event 为 nil")
	}
	if ev.TaskID == "" || ev.InvocationID == "" {
		return errors.New("storage: usage_event 缺少 task_id/invocation_id")
	}
	if ev.Status == "" {
		ev.Status = UsageSettled
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}
	// quietDB：同键重放是预期幂等路径，gorm 默认 logger 会把唯一键冲突打红
	// ERROR（重试风暴下刷屏），与 MarkCommentDelivery 同款降噪。
	err := s.quietDB().WithContext(ctx).Create(ev).Error
	if isDuplicateKeyErr(err) {
		return nil
	}
	return err
}

// taskUsageEvents 的包级实现（转发壳见原文件）。
func taskUsageEvents(s *Store, ctx context.Context, taskID string) ([]UsageEvent, error) {
	var out []UsageEvent
	err := s.quietDB().WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("occurred_at ASC, id ASC").Find(&out).Error
	return out, err
}

// reconcileUsageEvents 的包级实现（转发壳见原文件）。
func reconcileUsageEvents(s *Store, ctx context.Context) (int, error) {
	var tasks []Task
	if err := s.quietDB().WithContext(ctx).
		Select("id", "platform", "owner", "repo", "attempt", "tokens_used", "created_at", "finished_at").
		Where("status IN ? AND tokens_used > 0 "+
			"AND NOT EXISTS (SELECT 1 FROM usage_events WHERE usage_events.task_id = tasks.id)",
			[]string{StatusDone, StatusFailed, StatusStale}).
		Find(&tasks).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, tk := range tasks {
		occurred := tk.CreatedAt
		if tk.FinishedAt != nil {
			occurred = *tk.FinishedAt
		}
		if err := s.RecordUsageEvent(ctx, &UsageEvent{
			TaskID: tk.ID, AttemptID: strconv.Itoa(tk.Attempt),
			InvocationID: "backfill:" + tk.ID,
			Platform:     tk.Platform, Owner: tk.Owner, Repo: tk.Repo,
			Provider: "backfill", InputTokens: tk.TokensUsed,
			Status: UsageSettled, OccurredAt: occurred,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// backfillTaskUsageDelta 的包级实现（转发壳见原文件）。
func backfillTaskUsageDelta(s *Store, ctx context.Context, taskID, platform, owner, repo string,
	attempt, tokensUsed int, now time.Time) error {
	var recorded int64
	if err := s.quietDB().WithContext(ctx).Model(&UsageEvent{}).
		Where("task_id = ? AND attempt_id = ? AND status = ?",
			taskID, strconv.Itoa(attempt), UsageSettled).
		Select("COALESCE(SUM(input_tokens + output_tokens),0)").Scan(&recorded).Error; err != nil {
		return err
	}
	delta := int64(tokensUsed) - recorded
	if delta <= 0 {
		return nil
	}
	return s.RecordUsageEvent(ctx, &UsageEvent{
		TaskID: taskID, AttemptID: strconv.Itoa(attempt),
		InvocationID: fmt.Sprintf("backfill:%s:%d", taskID, attempt),
		Platform:     platform, Owner: owner, Repo: repo,
		Provider: "backfill", InputTokens: int(delta),
		Status: UsageSettled, OccurredAt: now,
	})
}
