// service_impl_taskapi.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/oekit/pkg/ptrx"
	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// createTask 的包级实现（转发壳见原文件）。
func createTask(s *Service, ctx context.Context, req *argus.CreateTaskRequest) (*argus.CreateTaskResponse, int) {
	kind, err := kindFromIDL(req.Kind)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	return s.CreateTaskDirect(ctx, kind, req.Platform, req.Owner, req.Repo, req.Number, map[string]string(req.Artifacts))
}

// createDiffTask 的包级实现（转发壳见原文件）。
func createDiffTask(s *Service, ctx context.Context, diff string) (*argus.CreateTaskResponse, int) {
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

// createTaskDirect 的包级实现（转发壳见原文件）。
func createTaskDirect(s *Service, ctx context.Context, kind, platform, owner, repo, number string,
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
		s.audit(ctx, auditTaskCreate, "task_id", task.ID, "kind", kind, "source", "api",
			"platform", platform, "owner", owner, "repo", repo, "number", number,
			"token_fp", tfp)
	}
	return &argus.CreateTaskResponse{TaskId: task.ID, Status: task.Status}, http.StatusOK
}

// listTasks 的包级实现（转发壳见原文件）。
func listTasks(s *Service, ctx context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	switch status {
	case "", storage.StatusPending, storage.StatusRunning, storage.StatusDone,
		storage.StatusFailed, storage.StatusStale:
	default:
		return nil, http.StatusBadRequest
	}
	// #94：scoped token 的授权 pattern 下沉 SQL 前置过滤（limit 在过滤后
	// 生效，修「截断页」）；无 pattern = 原全量列举（单租户/无约束 token 零变化）。
	var tasks []storage.Task
	var err error
	if tp := TokenReposFromContext(ctx); tp != "" {
		if pats, ok := translateRepoPatterns(tp); ok {
			tasks, err = s.Store.ListTasksScoped(ctx, status, limit, pats)
		} else {
			tasks, err = s.Store.ListTasks(ctx, status, limit) // 异形 pattern 回落旧路径（内存后置过滤）
		}
	} else {
		tasks, err = s.Store.ListTasks(ctx, status, limit)
	}
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

// getTask 的包级实现（转发壳见原文件）。
func getTask(s *Service, ctx context.Context, taskID string) (*argus.GetTaskResponse, int) {
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
	// 回复控制结果（v8.5.0）：控制台回答「为什么没回帖 / 报告少了几条」。
	if task.ReplyState != "" {
		resp.ReplyState = strPtr(task.ReplyState)
	}
	if task.SilencedFloor > 0 {
		v := clampInt32(task.SilencedFloor)
		resp.SilencedFloor = &v
	}
	if task.SilencedCap > 0 {
		v := clampInt32(task.SilencedCap)
		resp.SilencedCap = &v
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

// publishCreated 的包级实现（转发壳见原文件）。
func publishCreated(s *Service, t *storage.Task) {
	if s.Bus == nil || t == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: progress.EventTaskCreated, TaskID: t.ID, Platform: t.Platform,
		Owner: t.Owner, Repo: t.Repo, Number: t.Number,
	})
}

// publishProgress 的包级实现（转发壳见原文件）。
func publishProgress(s *Service, ev progress.Event) {
	ev.ID = progress.NewEventID()
	if s.EventSeen != nil {
		s.EventSeen.Add(ev.ID) // 本地发布登记：fanout 回灌同 ID 跳过
	}
	s.Bus.Publish(ev)
	if s.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = tenant.NewContext(ctx, ev.Tenant) // 事件自带租户（#110：指标按租户归因）
	if err := s.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped(ctx, "outbox_failed")
		s.log().Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
}

// publishCommentReplied 的包级实现（转发壳见原文件）。
func publishCommentReplied(s *Service, status, platform, owner, repo, number string) {
	if s.Bus == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: "comment.replied", Status: status, Platform: platform,
		Owner: owner, Repo: repo, Number: number,
	})
}

// publishQADelta 的包级实现（转发壳见原文件）：
//   - DeltaFanout 开（默认，#67）：与可靠事件同管道——本地 Bus 即时 + Outbox
//     跨实例 fanout 回灌（EventSeen 登记，同实例 at-most-once）。双副本下任务
//     在 Pod A 执行、SSE 客户端连 Pod B 也能收到增量。
//   - DeltaFanout 关（ARGUS_DELTA_FANOUT=0 回滚）：旧 live-only——只发本地 Bus，
//     不写 outbox、不登记 EventSeen（行为与 v7.4.x 逐字一致）。
func publishQADelta(s *Service, ctx context.Context, platform, owner, repo, number string, d plugin.AgentDelta) {
	if s.Bus == nil {
		return
	}
	ev := progress.Event{
		Type: progress.EventAgentDelta, Tenant: tenant.FromContext(ctx), Platform: platform,
		Owner: owner, Repo: repo, Number: number,
		AgentPhase: d.Phase, AgentIteration: d.Iteration,
		Delta: d.Text, Seq: d.Seq, Final: d.Final,
	}
	if s.Cfg != nil && !s.Cfg.DeltaFanout {
		s.Bus.Publish(ev)
		return
	}
	s.publishProgress(ev)
}
