// store_task_query.go —— 任务查询面：单条/按 head/按 PR/报告与清单查询。

package storage

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// GetTask 按 ID 查询。无此任务返回 ErrNotFound 哨兵。
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) {
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

func (s *Store) GetTaskByEventID(ctx context.Context, eventID string) (*Task, error) {
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

// ListTasksByHead 查询同 PR 同 head 的任务（E2E 幂等断言/诊断用）。
func (s *Store) ListTasksByHead(ctx context.Context, platform, owner, repo, number, headSHA string) ([]Task, error) {
	var out []Task
	err := s.db.WithContext(ctx).Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND head_sha = ?",
		platform, owner, repo, number, headSHA).Order("created_at ASC").Find(&out).Error
	return out, err
}

// HasNewerNonFailedTask staleness 单条件判定（§10.2 五轮评审修正）：
// 存在同 PR 更晚创建的非 failed 且带 head 的任务 ⇒ 本任务 stale。
// 只统计 head_sha <> ” 的任务（webhook/poller 来源）：手动任务 head 为空（执行期才回填），
// 若它先压 stale 再失败（手动任务参数/平台错误率高），本轮审查会被永久静默吞掉。
// 数据全来自 tasks 表（head_sha/created_at），不依赖 review_targets.last_head_sha。
func (s *Store) HasNewerNonFailedTask(ctx context.Context, platform, owner, repo, number, taskID string, createdAt time.Time) (bool, error) {
	var cnt int64
	err := s.db.WithContext(ctx).Model(&Task{}).
		Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND id <> ? AND created_at > ? AND status <> ? AND head_sha <> ''",
			platform, owner, repo, number, taskID, createdAt, StatusFailed).
		Count(&cnt).Error
	return cnt > 0, err
}

// TaskCounts 队列深度（pending/running），供指标暴露。
func (s *Store) TaskCounts(ctx context.Context) (pending, running int64, err error) {
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusPending).Count(&pending).Error
	if err != nil {
		return 0, 0, err
	}
	err = s.quietDB().WithContext(ctx).Model(&Task{}).
		Where("status = ?", StatusRunning).Count(&running).Error
	return pending, running, err
}

// ListTasks 任务列表（GET /tasks）：status 过滤（空=全部），created_at 倒序，limit 收口。
// Select 摘要列（深挖修复）：此前 Find 全列——report/result_json/debug_json 均为
// LONGTEXT，单行可达 MB 级，列表 100 条白拉几百 MB IO，还放大 SQLite 单写连接的
// 队头阻塞。报告/调试产物只为详情接口服务。
func (s *Store) ListTasks(ctx context.Context, status string, limit int) ([]Task, error) {
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

// ListTasksByPR 按 PR 查任务（新→旧，摘要列）。@bot status 指令取最近一轮
// 状态与结论用；limit 缺省 5（status 只看最近几轮）。
func (s *Store) ListTasksByPR(ctx context.Context, platform, owner, repo, number string, limit int) ([]Task, error) {
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

// FindReportByHead 按 head SHA 查最近一次成功的报告（GET /reports/:ref）。
// 无报告返回 ErrNotFound 哨兵。
//
// v6.12 P1（外部评估实锤）：此前 head SHA 是全局寻址键，同 SHA 多仓（fork/
// 镜像）时可能先取到别仓报告再 403，而不是继续查找有权访问的报告。现改
// 组合寻址——platform/owner/repo 非空时在查询阶段限定仓库范围，天然只取到
// 本仓报告；全空保持全局语义（向后兼容）。
func (s *Store) FindReportByHead(ctx context.Context, headSHA, platform, owner, repo string) (*Task, error) {
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

// FindReportsByHead 按 head SHA 查全部成功报告（created_at DESC，跨仓 SHA 碰撞
// 授权回退用：取列表逐条过授权，第一条通过的返回）。无报告返回空切片。
func (s *Store) FindReportsByHead(ctx context.Context, headSHA string, limit int) ([]Task, error) {
	if limit <= 0 || limit > 20 {
		limit = 10
	}
	var ts []Task
	err := s.quietDB().WithContext(ctx).
		Where("head_sha = ? AND status = ? AND report != ''", headSHA, StatusDone).
		Order("created_at DESC").Limit(limit).Find(&ts).Error
	return ts, err
}

// GetLastReviewedDiff 查询同 PR 最近一次 done 任务的 diff 文本（增量审查 diff-of-diffs）。
// 返回空字符串表示无前置审查记录。
func (s *Store) GetLastReviewedDiff(ctx context.Context, platform, owner, repo, number string) (string, error) {
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
