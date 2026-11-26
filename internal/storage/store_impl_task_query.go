// store_impl_task_query.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/argus/internal/tenant"
)

// mergeTaskStageArtifacts 的包级实现（转发壳见原文件）。
func mergeTaskStageArtifacts(s *Store, ctx context.Context, id string, patch map[string]string) error {
	return s.withWrite(func() error {
		var row Task
		if err := s.db.WithContext(ctx).Select("stage_artifacts").Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		cur := map[string]string{}
		if row.StageArtifacts != "" {
			_ = json.Unmarshal([]byte(row.StageArtifacts), &cur) // 坏 JSON 当空处理（best-effort 读，留痕：不阻断主路）
		}
		if cur == nil {
			cur = map[string]string{}
		}
		for k, v := range patch {
			cur[k] = v
		}
		b, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("stage_artifacts", string(b)).Error
	})
}

// mergeTaskArtifacts 的包级实现（转发壳见原文件）。
func mergeTaskArtifacts(s *Store, ctx context.Context, id string, patch map[string]string) error {
	return s.withWrite(func() error {
		var row Task
		if err := s.db.WithContext(ctx).Select("artifacts_json").Where("id = ?", id).First(&row).Error; err != nil {
			return err
		}
		cur := map[string]string{}
		if row.ArtifactsJSON != "" {
			// "null"/坏 JSON 均可能把 cur 反序列化成 nil——统一在下文钳回空表
			_ = json.Unmarshal([]byte(row.ArtifactsJSON), &cur) // 坏 JSON 当空处理（best-effort 读，留痕：不阻断主路）
		}
		if cur == nil {
			cur = map[string]string{}
		}
		for k, v := range patch {
			cur[k] = v
		}
		b, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		return s.db.WithContext(ctx).Model(&Task{}).Where("id = ?", id).
			Update("artifacts_json", string(b)).Error
	})
}

// getTask 的包级实现（转发壳见原文件）。
func getTask(s *Store, ctx context.Context, id string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// getTaskByEventID 的包级实现（转发壳见原文件）。
func getTaskByEventID(s *Store, ctx context.Context, eventID string) (*Task, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).Where("event_id = ?", eventID).First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// listTasksByHead 的包级实现（转发壳见原文件）。
func listTasksByHead(s *Store, ctx context.Context, platform, owner, repo, number, headSHA string) ([]Task, error) {
	var out []Task
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
		platform, owner, repo, number, headSHA).Order("created_at ASC").Find(&out).Error
	return out, err
}

// hasNewerNonFailedTask 的包级实现（转发壳见原文件）。
func hasNewerNonFailedTask(s *Store, ctx context.Context, platform, owner, repo, number, taskID string, createdAt time.Time) (bool, error) {
	var cnt int64
	err := s.db.WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND id <> ? AND created_at > ? AND status <> ? AND head_sha <> ''",
			platform, owner, repo, number, taskID, createdAt, StatusFailed).
		Count(&cnt).Error
	return cnt > 0, err
}

// taskCountsByTenant 的包级实现（转发壳见原文件）。
// #110 租户维度：GROUP BY tenant_id × status 全量观测——必须显式 WithoutScope
// （租户谓词只给 default，跨租户计数靠它放行；静默按 default 过滤是本次审计
// 揪出的真缺陷：旧全局 gauge 实锤只数了 default 租户）。
func taskCountsByTenant(s *Store, ctx context.Context) (map[string][2]int64, error) {
	rows := []struct {
		TenantID string
		Status   string
		Cnt      int64
	}{}
	err := s.quietDB().WithContext(tenant.WithoutScope(ctx)).Model(&Task{}).
		Select("tenant_id, status, COUNT(*) AS cnt").
		Where("status IN ?", []string{StatusPending, StatusRunning}).
		Group("tenant_id").Group("status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string][2]int64, len(rows))
	for _, r := range rows {
		d := out[r.TenantID]
		if r.Status == StatusPending {
			d[0] = r.Cnt
		} else {
			d[1] = r.Cnt
		}
		out[r.TenantID] = d
	}
	return out, nil
}

// listTasks 的包级实现（转发壳见原文件）。
func listTasks(s *Store, ctx context.Context, status string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	q := s.quietDB().WithContext(ctx).
		Select("id", "kind", "platform", "owner", "repo", "number", "head_sha", "event_type",
			"status", "verdict", "runner_id", "summary_comment_id", "tokens_used", "artifacts_json",
			"created_at", "started_at", "finished_at", "heartbeat_at", "updated_at").
		Order("created_at DESC, id DESC").Limit(limit)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []Task
	err := q.Find(&out).Error
	return out, err
}

// taskSummaryColumns 清单摘要列（LONGTEXT 报告/调试列不进列表，见 listTasks 注释）。
var taskSummaryColumns = []string{
	"id", "kind", "platform", "owner", "repo", "number", "head_sha", "event_type",
	"status", "verdict", "runner_id", "summary_comment_id", "tokens_used", "artifacts_json",
	"created_at", "started_at", "finished_at", "heartbeat_at", "updated_at",
}

// applyTaskListFilter 业务过滤条件拼装（等值 + 关键词模糊；零值跳过）。
func applyTaskListFilter(q *gorm.DB, f TaskListFilter) *gorm.DB {
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Platform != "" {
		q = q.Where("platform = ?", f.Platform)
	}
	if f.Owner != "" {
		q = q.Where("owner = ?", f.Owner)
	}
	if f.Repo != "" {
		q = q.Where("repo = ?", f.Repo)
	}
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if kw := strings.TrimSpace(f.Q); kw != "" {
		pat := "%" + likeEscape(kw) + "%"
		q = q.Where("(id LIKE ? ESCAPE '\\' OR number LIKE ? ESCAPE '\\' OR head_sha LIKE ? ESCAPE '\\')", pat, pat, pat)
	}
	return q
}

// clampTaskListLimit 清单 limit 收口（0→20，>100→100）。
func clampTaskListLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// listTasksFiltered 的包级实现（v8.8.2 控制台筛选面）。
func listTasksFiltered(s *Store, ctx context.Context, f TaskListFilter) ([]Task, error) {
	q := s.quietDB().WithContext(ctx).Select(taskSummaryColumns).
		Order("created_at DESC, id DESC").Limit(clampTaskListLimit(f.Limit))
	q = applyTaskListFilter(q, f)
	var out []Task
	err := q.Find(&out).Error
	return out, err
}

// listTasksByPR 的包级实现（转发壳见原文件）。
func listTasksByPR(s *Store, ctx context.Context, platform, owner, repo, number string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 50 {
		limit = 50
	}
	var out []Task
	err := s.quietDB().WithContext(ctx).
		Select("id", "kind", "platform", "owner", "repo", "number", "head_sha", "event_type",
			"status", "verdict", "runner_id", "summary_comment_id", "tokens_used", "artifacts_json",
			"created_at", "started_at", "finished_at", "heartbeat_at", "updated_at").
		Where("platform = ? AND owner = ? AND repo = ? AND number = ?", platform, owner, repo, number).
		Order("created_at DESC, id DESC").Limit(limit).
		Find(&out).Error
	return out, err
}

// findReportByHead 的包级实现（转发壳见原文件）。
func findReportByHead(s *Store, ctx context.Context, headSHA, platform, owner, repo string) (*Task, error) {
	q := s.quietDB().WithContext(ctx).
		Where("head_sha = ? AND status = ? AND report != ''", headSHA, StatusDone)
	if platform != "" || owner != "" || repo != "" {
		q = q.Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo)
	}
	var t Task
	err := q.Order("created_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &t, nil
}

// findReportsByHead 的包级实现（转发壳见原文件）。
func findReportsByHead(s *Store, ctx context.Context, headSHA string, limit int) ([]Task, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var ts []Task
	err := s.quietDB().WithContext(ctx).
		Where("head_sha = ? AND status = ? AND report != ''", headSHA, StatusDone).
		Order("created_at DESC").Limit(limit).Find(&ts).Error
	return ts, err
}

// getLastReviewedDiff 的包级实现（转发壳见原文件）。
func getLastReviewedDiff(s *Store, ctx context.Context, platform, owner, repo, number string) (string, error) {
	var t Task
	err := s.quietDB().WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ?",
			platform, owner, repo, number, StatusDone).
		Order("created_at DESC").First(&t).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil
		}
		return "", err
	}
	if t.ResultJSON == "" {
		return "", nil
	}
	// 从 result_json 中提取 diff_text 字段（轻量解析，不反序列化整个结构）
	var payload struct {
		DiffText string `json:"diff_text"`
	}
	if err := jsonUnmarshal([]byte(t.ResultJSON), &payload); err != nil {
		return "", nil // result_json 格式异常不阻塞审查
	}
	return payload.DiffText, nil
}

// lastDoneTaskFinishedAt 的包级实现（转发壳见原文件）。
func lastDoneTaskFinishedAt(s *Store, ctx context.Context, platform, owner, repo, number string) (time.Time, bool, error) {
	var t Task
	err := s.db.WithContext(ctx).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND status = ? AND finished_at IS NOT NULL",
			platform, owner, repo, number, StatusDone).
		Order("finished_at DESC").First(&t).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	if t.FinishedAt == nil {
		return time.Time{}, false, nil
	}
	return *t.FinishedAt, true, nil
}
