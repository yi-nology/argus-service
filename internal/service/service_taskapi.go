// service_taskapi.go —— TaskAPI 任务面：任务创建/清单/详情
// （抑制见 service_taskapi_suppress.go，事件见 service_taskapi_events.go，
// 用量/报告读取见 service_taskapi_read.go）。

package service

import (
	"context"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// CreateTask CLI/其他系统手动提交审查任务（🔒）。与 webhook 任务同语义（§5.1 规则 5）。
func (s *Service) CreateTask(ctx context.Context, req *argus.CreateTaskRequest) (*argus.CreateTaskResponse, int) {
	return createTask(s, ctx, req)
}

// diffTaskMaxBytes diff_preview 任务输入上限（48KB）：artifacts_json 是 MySQL
// TEXT 列（64KB 上界），JSON 转义对换行密集的 diff 还有约一成膨胀——48KB 留足
// 余量。同步 review_diff 的 120KB 上限不适用（纯内存不落库）。MCP 侧错误文案
// 的"48KB"与本值同源手写，改此值须同步。
const diffTaskMaxBytes = 48 * 1024

// CreateDiffTask 本地 diff 异步预审任务（v3.123.0，MCP review_diff_async 数据面）：
// 无 PR 语义——platform 记 local、四元组空，diff 文本存 artifacts（ArtifactDiffInput），
// runner 按 kind 在 prepareContext 之前分派单轮写作执行体，报告落库供提交方
// get_report 轮询，不回帖。不做 PR 维去重：每次提交都是显式意图，各自独立任务。
func (s *Service) CreateDiffTask(ctx context.Context, diff string) (*argus.CreateTaskResponse, int) {
	return createDiffTask(s, ctx, diff)
}

// CreateTaskDirect 核心建任务（kind 为字符串词表），供 handler 与 MCP server 共用。
func (s *Service) CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
	artifacts map[string]string) (*argus.CreateTaskResponse, int) {
	return createTaskDirect(s, ctx, kind, platform, owner, repo, number, artifacts)
}

// ListTasks 任务列表（🔒）：status 过滤 + limit 收口，created_at 倒序。
func (s *Service) ListTasks(ctx context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	return listTasks(s, ctx, status, limit)
}

// ListTasksQuery 带筛选的任务列表（控制台：状态/仓库/类型/关键词，v8.8.2）。
func (s *Service) ListTasksQuery(ctx context.Context, f storage.TaskListFilter) (*argus.ListTasksResponse, int) {
	return listTasksFiltered(s, ctx, f)
}

// GetTask 查询任务状态与结果（报告 markdown 内联返回，§5；🔒）。
func (s *Service) GetTask(ctx context.Context, taskID string) (*argus.GetTaskResponse, int) {
	return getTask(s, ctx, taskID)
}
