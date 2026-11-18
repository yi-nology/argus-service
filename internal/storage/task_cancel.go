// task_cancel.go —— 控制面终止任务（#48）：立即 failed + 取消自动重试。
//
// 终止语义（不伪造「强杀」）：
//   - 立即把 pending/running 置 failed：runner 在途的 FinishTaskFenced 带
//     status='running' + runner_id 条件，控制面先改了状态后其终态写入天然被围栏，
//     不会覆盖控制面的结论；
//   - next_retry_at=NULL：ResetFailedForRetry 只重置「failed 且 next_retry_at 非空
//     且到期」的任务，置空即永久阻断自动重试（人工重审仍可另建任务）；
//   - cancel_requested_at 非空 = 终止已受理，既供 runner 检查点只读协作停机，
//     也承担重复调用幂等（第二次返回 cancelled=false）。
//
// 在途 LLM/工具调用无法强杀：runner 只会在下一个检查点停止产生副作用；检查点
// 之间已发生的平台写操作由既有 sideeffect 闸/幂等键兜底（见 internal/runner/cancel.go）。
package storage

import (
	"context"
	"errors"
	"fmt"
	"github.com/yi-nology/argus/internal/tenant"
	"time"

	"gorm.io/gorm"
)

// ErrTaskNotCancelable 任务当前状态不接受终止（done/stale/非操作员终止的 failed）。
var ErrTaskNotCancelable = errors.New("storage: 任务当前状态不可终止")

// TaskCancelResult 终止结果：Status 是控制面写入/观察到的任务状态；
// Cancelled=false 表示本次是重复终止（任务此前已因操作员终止而 failed）。
type TaskCancelResult struct {
	Status    string
	Cancelled bool
}

// defaultCancelReason 操作员未提供理由时的审计留痕文案。
const defaultCancelReason = "operator cancel"

// RequestTaskCancel 终止 pending/running 任务（单条条件更新，天然原子）：
//
//	UPDATE tasks SET status='failed', fail_reason=?, cancel_requested_at=?,
//	                 next_retry_at=NULL, finished_at=?
//	WHERE id=? AND status IN ('pending','running')
//
// 命中 → {failed, true}。未命中时按行态分类：
//   - 任务不存在 → ErrNotFound；
//   - 已 failed 且 cancel_requested_at 非空 → 幂等返回 {failed, false}, nil；
//   - 其余（done/stale/failed 但非操作员终止）→ ErrTaskNotCancelable。
func (s *Store) RequestTaskCancel(ctx context.Context, id, reason string) (TaskCancelResult, error) {
	// 控制面全局操作（#88/#100）：操作员终止不限租户——授权在控制面 admin 门，
	// 存储面按 ID 直取（谓词注入会让非 default 租户任务取消静默落空）。
	ctx = tenant.WithoutScope(ctx)
	if reason == "" {
		reason = defaultCancelReason
	}
	failReason := "操作员终止: " + reason
	var (
		out           TaskCancelResult
		notFound      bool
		notCancelable bool
	)
	err := s.withWrite(func() error {
		now := time.Now()
		res := s.db.WithContext(ctx).Model(&Task{}).
			Where("id = ? AND status IN ?", id, []string{StatusPending, StatusRunning}).
			Updates(map[string]any{
				"status":              StatusFailed,
				"fail_reason":         failReason,
				"cancel_requested_at": &now,
				"next_retry_at":       nil,
				"finished_at":         &now,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			out = TaskCancelResult{Status: StatusFailed, Cancelled: true}
			return nil
		}
		// 未命中：读当前行态做分类（不存在/幂等/不可终止）。
		var row Task
		qerr := s.db.WithContext(ctx).
			Select("id", "status", "cancel_requested_at").
			Where("id = ?", id).Take(&row).Error
		if errors.Is(qerr, gorm.ErrRecordNotFound) {
			notFound = true
			return nil
		}
		if qerr != nil {
			return qerr
		}
		if row.Status == StatusFailed && row.CancelRequestedAt != nil {
			out = TaskCancelResult{Status: StatusFailed, Cancelled: false}
			return nil
		}
		notCancelable = true
		return nil
	})
	if err != nil {
		return TaskCancelResult{}, fmt.Errorf("storage: 终止任务 %q 失败: %w", id, err)
	}
	if notFound {
		return TaskCancelResult{}, fmt.Errorf("storage: 任务 %q: %w", id, ErrNotFound)
	}
	if notCancelable {
		return TaskCancelResult{}, fmt.Errorf("storage: 任务 %q 当前状态不可终止: %w", id, ErrTaskNotCancelable)
	}
	return out, nil
}

// TaskCancelRequested 只读终止标记（runner 检查点用）：cancel_requested_at 非空
// 即为 true。任务不存在返回 false, nil——检查点是尽力而为的协作停止，不应因
// 行被清理而把 runner 打成失败。
func (s *Store) TaskCancelRequested(ctx context.Context, id string) (bool, error) {
	var row Task
	err := s.db.WithContext(ctx).
		Select("id", "cancel_requested_at").
		Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: 读任务终止标记失败: %w", err)
	}
	return row.CancelRequestedAt != nil, nil
}
