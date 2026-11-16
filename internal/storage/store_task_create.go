// store_task_create.go —— 任务创建：工厂、webhook 同 SHA 去重、TaskAPI 幂等直建。

package storage

import (
	"context"
	"time"
)

// CreateTask 新建任务（CLI/TaskAPI 提交；无幂等键）。
func (s *Store) CreateTask(ctx context.Context, t *Task) error { return createTask(s, ctx, t) }

// NewTask 建任务工厂（v3.109.0）：ID/Status/CreatedAt 三个不变量此前在
// taskapi/webhook×2/poller 四处字面量各自组装，漏一处即幻影 ID/缺时间戳的
// 形态漂移且无从测红。调用方补 kind 专属字段（HeadSHA/EventType/Priority/
// EventID/ArtifactsJSON）。
func NewTask(kind, platform, owner, repo, number string) *Task {
	return &Task{
		ID:        NewTaskID(),
		Kind:      kind,
		Platform:  platform,
		Owner:     owner,
		Repo:      repo,
		Number:    number,
		Status:    StatusPending,
		CreatedAt: time.Now(),
	}
}

// 任务去重原因词表（CreateTaskForWebhook 返回值；webhook 侧按此分发——
// 此前生产与比较两侧共 8 处裸字面量）。
const (
	DupLastHeadSHA = "last_head_sha"
	DupEventID     = "event_id"
	DupInFlight    = "in_flight"
)

// CreateTaskForWebhook 同步完成"同 SHA 去重检查 + 落库"（同一事务，§5.1 规则 4 的 TOCTOU 防线）。
// 返回 dupReason 非空表示命中去重（Dup* 词表），existingID 给出可指向的任务。
func (s *Store) CreateTaskForWebhook(ctx context.Context, t *Task) (dupReason, existingID string, err error) {
	return createTaskForWebhook(s, ctx, t)
}

// CreateTaskDirectIfIdle TaskAPI 直接触发路径的幂等建任务：同 platform/owner/repo/number
// 已存在 pending/running 任务时不新建，返回既有任务（created=false）。
// 与 CreateTaskForWebhook 的 in_flight 去重语义对齐，防手工触发与轮询/webhook 竞态双审。
// done 任务不拦——手工强制重审是 TaskAPI 的合法用途。
func (s *Store) CreateTaskDirectIfIdle(ctx context.Context, t *Task) (existing *Task, created bool, err error) {
	return createTaskDirectIfIdle(s, ctx, t)
}
