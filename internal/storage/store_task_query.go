// store_task_query.go —— 任务查询面：单条/按 head/按 PR/报告与清单查询。

package storage

import (
	"context"
	"time"
)

// GetTask 按 ID 查询。无此任务返回 ErrNotFound 哨兵。
func (s *Store) GetTask(ctx context.Context, id string) (*Task, error) { return getTask(s, ctx, id) }

func (s *Store) GetTaskByEventID(ctx context.Context, eventID string) (*Task, error) {
	return getTaskByEventID(s, ctx, eventID)
}

// ListTasksByHead 查询同 PR 同 head 的任务（E2E 幂等断言/诊断用）。
func (s *Store) ListTasksByHead(ctx context.Context, platform, owner, repo, number, headSHA string) ([]Task, error) {
	return listTasksByHead(s, ctx, platform, owner, repo, number, headSHA)
}

// HasNewerNonFailedTask staleness 单条件判定（§10.2 五轮评审修正）：
// 存在同 PR 更晚创建的非 failed 且带 head 的任务 ⇒ 本任务 stale。
// 只统计 head_sha <> ” 的任务（webhook/poller 来源）：手动任务 head 为空（执行期才回填），
// 若它先压 stale 再失败（手动任务参数/平台错误率高），本轮审查会被永久静默吞掉。
// 数据全来自 tasks 表（head_sha/created_at），不依赖 review_targets.last_head_sha。
func (s *Store) HasNewerNonFailedTask(ctx context.Context, platform, owner, repo, number, taskID string, createdAt time.Time) (bool, error) {
	return hasNewerNonFailedTask(s, ctx, platform, owner, repo, number, taskID, createdAt)
}

// TaskCountsByTenant 队列深度（pending/running）按租户全量（#110），供
// per-tenant 指标暴露。跨租户计数显式 WithoutScope（见包级实现注释）。
func (s *Store) TaskCountsByTenant(ctx context.Context) (map[string][2]int64, error) {
	return taskCountsByTenant(s, ctx)
}

// ListTasks 任务列表（GET /tasks）：status 过滤（空=全部），created_at 倒序，limit 收口。
// Select 摘要列（深挖修复）：此前 Find 全列——report/result_json/debug_json 均为
// LONGTEXT，单行可达 MB 级，列表 100 条白拉几百 MB IO，还放大 SQLite 单写连接的
// 队头阻塞。报告/调试产物只为详情接口服务。
func (s *Store) ListTasks(ctx context.Context, status string, limit int) ([]Task, error) {
	return listTasks(s, ctx, status, limit)
}

// ListTasksByPR 按 PR 查任务（新→旧，摘要列）。@bot status 指令取最近一轮
// 状态与结论用；limit 缺省 5（status 只看最近几轮）。
func (s *Store) ListTasksByPR(ctx context.Context, platform, owner, repo, number string, limit int) ([]Task, error) {
	return listTasksByPR(s, ctx, platform, owner, repo, number, limit)
}

// FindReportByHead 按 head SHA 查最近一次成功的报告（GET /reports/:ref）。
// 无报告返回 ErrNotFound 哨兵。
//
// v6.12 P1（外部评估实锤）：此前 head SHA 是全局寻址键，同 SHA 多仓（fork/
// 镜像）时可能先取到别仓报告再 403，而不是继续查找有权访问的报告。现改
// 组合寻址——platform/owner/repo 非空时在查询阶段限定仓库范围，天然只取到
// 本仓报告；全空保持全局语义（向后兼容）。
func (s *Store) FindReportByHead(ctx context.Context, headSHA, platform, owner, repo string) (*Task, error) {
	return findReportByHead(s, ctx, headSHA, platform, owner, repo)
}

// FindReportsByHead 按 head SHA 查全部成功报告（created_at DESC，跨仓 SHA 碰撞
// 授权回退用：取列表逐条过授权，第一条通过的返回）。无报告返回空切片。
func (s *Store) FindReportsByHead(ctx context.Context, headSHA string, limit int) ([]Task, error) {
	return findReportsByHead(s, ctx, headSHA, limit)
}

// GetLastReviewedDiff 查询同 PR 最近一次 done 任务的 diff 文本（增量审查 diff-of-diffs）。
// 返回空字符串表示无前置审查记录。
func (s *Store) GetLastReviewedDiff(ctx context.Context, platform, owner, repo, number string) (string, error) {
	return getLastReviewedDiff(s, ctx, platform, owner, repo, number)
}
