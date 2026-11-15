// service_taskapi.go —— TaskAPI 任务面：任务创建/清单/详情
// （抑制见 service_taskapi_suppress.go，事件见 service_taskapi_events.go，
// 用量/报告读取见 service_taskapi_read.go）。

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"

	"github.com/oekit/pkg/ptrx"
)

// CreateTask CLI/其他系统手动提交审查任务（🔒）。与 webhook 任务同语义（§5.1 规则 5）。
func (s *Service) CreateTask(ctx context.Context, req *argus.CreateTaskRequest) (*argus.CreateTaskResponse, int) {
	kind, err := kindFromIDL(req.Kind)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	return s.CreateTaskDirect(ctx, kind, req.Platform, req.Owner, req.Repo, req.Number, map[string]string(req.Artifacts))
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
	if s.LLM == nil {
		return nil, http.StatusServiceUnavailable
	}
	if strings.TrimSpace(diff) == "" || len(diff) > diffTaskMaxBytes {
		return nil, http.StatusBadRequest
	}
	artifacts, _ := json.Marshal(map[string]string{plugin.ArtifactDiffInput: diff})
	task := storage.NewTask(domain.TaskKindDiffPreview, "local", "", "", "")
	task.ArtifactsJSON = string(artifacts)
	task.Priority = 10 // 人类在等（提交方拿着 task_id 轮询）
	if err := s.Store.CreateTask(ctx, task); err != nil {
		s.log().Error("argus.argus.diff_task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.publishCreated(task)
	s.log().Info("argus.argus.diff_task_created", "task", task.ID, "bytes", len(diff))
	return &argus.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// CreateTaskDirect 核心建任务（kind 为字符串词表），供 handler 与 MCP server 共用。
func (s *Service) CreateTaskDirect(ctx context.Context, kind, platform, owner, repo, number string,
	artifacts map[string]string) (*argus.CreateTaskResponse, int) {
	// kind 白名单走 plugin 词表注册表单源（domain/taskkind.go canonical）。
	if !s.repoAllowedFor(ctx, platform, owner, repo) {
		return nil, http.StatusForbidden // ARGUS_REPO_ALLOWLIST 白名单（v3.134.0）
	}
	if !domain.IsKnownKind(kind) {
		return nil, http.StatusBadRequest
	}
	if _, _, ok := s.requirePlatform(platform); !ok {
		return nil, http.StatusBadRequest
	}
	if owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	// owner/repo 白名单（2026-10-01 评测补缺）：平台标识允许字母数字、. _ - 与
	// 子组斜杠（GitLab 多级组），拒掉引号/空白/控制符等进 API URL 的危险形态——
	// 此前任意字符串可入库并进入平台查询路径（评测实测 SQL 注入形态被建任务）。
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return nil, http.StatusBadRequest
	}
	// number 必须是纯十进制数字（平台 PR 序号）；strconv.Atoi 会放过 "+123" 等带符号写法，
	// 入库后运行期平台查询才失败。长度上限 10：超出 int64 的 20 位数字此前也会被
	// 建任务白跑一轮（评测实测）。
	if !isDigits(number) || len(number) > 10 {
		return nil, http.StatusBadRequest
	}
	// artifacts 总大小上限：任意大小 JSON 此前可直灌 DB（DoS 面）。
	if len(artifacts) > 0 {
		artifactsJSON, aerr := json.Marshal(artifacts)
		if aerr != nil || len(artifactsJSON) > artifactsMaxBytes {
			return nil, http.StatusBadRequest
		}
	}
	artifactsJSON, _ := json.Marshal(artifacts)
	task := storage.NewTask(kind, platform, owner, repo, number)
	task.ArtifactsJSON = string(artifactsJSON)
	// 人类在等（评论重审/API 手工/MCP）：优先于 poller 批量认领（v3.40.0）
	task.Priority = 10
	existing, created, err := s.Store.CreateTaskDirectIfIdle(ctx, task)
	if err != nil {
		s.log().Error("argus.argus.task_create_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	if !created {
		// 同 PR 已有在飞任务：幂等返回既有任务，不再叠加一份重复审查（token 白烧）。
		s.log().Info("argus.argus.task_create_deduped", "existing", existing.ID,
			"status", existing.Status, "pr", owner+"/"+repo+"#"+number)
		return &argus.CreateTaskResponse{TaskId: existing.ID, Status: existing.Status, Duplicated: ptrx.Of(true)}, http.StatusOK
	}
	s.publishCreated(task)

	s.log().Info("argus.argus.task_created", "task", task.ID, "kind", kind,
		"pr", task.Owner+"/"+task.Repo+"#"+task.Number)
	if tfp := TokenFPFromContext(ctx); tfp != "" {
		// 外部 API 建任务取证（v3.137.0）：哪条 token 建的什么任务——webhook/
		// poller 内部路径无 token 指纹，不产生噪音。
		s.Audit.Log(auditTaskCreate, "task_id", task.ID, "kind", kind, "source", "api",
			"platform", platform, "owner", owner, "repo", repo, "number", number,
			"token_fp", tfp)
	}
	return &argus.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// ListTasks 任务列表（🔒）：status 过滤 + limit 收口，created_at 倒序。
func (s *Service) ListTasks(ctx context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	switch status {
	case "", storage.StatusPending, storage.StatusRunning, storage.StatusDone,
		storage.StatusFailed, storage.StatusStale:
	default:
		return nil, http.StatusBadRequest
	}
	tasks, err := s.Store.ListTasks(ctx, status, limit)
	if err != nil {
		s.log().Error("argus.argus.list_tasks_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	// 预分配空切片：nil 会被序列化成 "tasks":null，与 IDL required list 语义不符
	resp := &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{}}
	for i := range tasks {
		t := &tasks[i]
		if !s.taskListable(ctx, t.Platform, t.Owner, t.Repo) {
			continue // 数据可见性隔离（v3.140.0）：域外任务不进清单（含 local 元数据）
		}
		item := &argus.TaskSummary{
			TaskId:    t.ID,
			Status:    t.Status,
			Kind:      t.Kind,
			Platform:  t.Platform,
			Owner:     t.Owner,
			Repo:      t.Repo,
			Number:    t.Number,
			CreatedAt: t.CreatedAt.Unix(),
		}
		if t.HeadSHA != "" {
			item.HeadSha = strPtr(t.HeadSHA)
		}
		if t.Verdict != "" {
			item.Verdict = strPtr(t.Verdict)
		}
		resp.Tasks = append(resp.Tasks, item)
	}
	return resp, http.StatusOK
}

// GetTask 查询任务状态与结果（报告 markdown 内联返回，§5；🔒）。
func (s *Service) GetTask(ctx context.Context, taskID string) (*argus.GetTaskResponse, int) {
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.taskVisible(ctx, task.Platform, task.Owner, task.Repo) {
		return nil, http.StatusForbidden // 数据可见性隔离（v3.140.0）：token 仓库域外任务
	}
	resp := &argus.GetTaskResponse{
		TaskId: task.ID,
		Status: task.Status,
	}
	if task.Verdict != "" {
		resp.Verdict = strPtr(task.Verdict)
	}
	if task.FailReason != "" {
		resp.FailReason = strPtr(task.FailReason)
	}
	if task.Report != "" {
		resp.Report = strPtr(task.Report)
	}
	if findings, ok := taskFindings(task.ResultJSON); ok {
		for _, f := range findings {
			by := f.By
			fw := &argus.Finding{
				File:     f.File,
				Line:     clampInt32(f.Line),
				Severity: f.Severity,
				Comment:  f.Comment,
				By:       strPtr(by),
				// 证据族（v7.3.x thrift 对称补齐）：与 MCP findingDTO 同源同语义——
				// Evidence 自由文本、EvidenceStatus 四态（空=未核验）。
				Evidence:       strPtr(f.Evidence),
				EvidenceStatus: strPtr(string(f.EvidenceStatus)),
			}
			resp.Findings = append(resp.Findings, fw)
		}
	}
	if task.DebugJSON != "" {
		resp.Debug = strPtr(task.DebugJSON)
	}
	return resp, http.StatusOK
}
