// service_fix_push.go —— 修复推送 preview→confirm（T4）HTTP 面。
//
// preview：只读投影最近任务的 await_confirm 就绪态 + 待审批票数。
// confirm：委托注入的 FixPushConfirm（bootstrap 接 runner.ConfirmFixPush），
// 绑定 task_id + expected_remote_head；仓库维度从任务记录解析。
package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// FixPushConfirmFunc confirm 执行体（bootstrap 注入 runner.ConfirmFixPush）。
// 返回 (status, message, headSHA, fixStatus, err)。
type FixPushConfirmFunc func(ctx context.Context, taskID, expectedHead string) (
	status, message, headSHA, fixStatus string, err error)

// FixPushPreview 只读预览（GET /fix-push/preview）。
func (s *Service) FixPushPreview(ctx context.Context, req *argus.FixPushPreviewRequest) (*argus.FixPushPreviewResponse, int) {
	return fixPushPreview(s, ctx, req)
}

// FixPushConfirm 确认推送（POST /fix-push/confirm）。
func (s *Service) FixPushConfirm(ctx context.Context, req *argus.FixPushConfirmRequest) (*argus.FixPushConfirmResponse, int) {
	return fixPushConfirm(s, ctx, req)
}

// fixPushStatusFailed confirm 响应状态词表（与 runner.FixPushConfirmResult 对齐；
// 非任务 StatusFailed——这是 API 裁决词，另起名避免 word-table 误钉）。
const (
	fixPushStatusFailed   = "failed"
	fixPushStatusRejected = "rejected"
)

func fixPushAdmin(ctx context.Context) bool { return ScopeFromContext(ctx) == "admin" }

func fixPushDeny(s *Service, ctx context.Context, action, subject string) {
	authzDeniedMetric(ctx, "fixpush."+action)
	s.log().Warn("argus.fixpush.denied", "action", action, "subject", subject,
		"token_fp", TokenFPFromContext(ctx))
}

// latestFixPushTask 取该 PR 最近一条带 FixStatus 语义的任务（优先 await_confirm）。
func latestFixPushTask(s *Service, ctx context.Context, platform, owner, repo, number string) (*storage.Task, error) {
	tasks, err := s.Store.ListTasksByPR(ctx, platform, owner, repo, number, 10)
	if err != nil {
		return nil, err
	}
	// 优先 await_confirm（GetTask 补全 fix_status——List 列表 Select 不含该列）。
	for _, t := range tasks {
		full, err := s.Store.GetTask(ctx, t.ID)
		if err != nil {
			continue
		}
		if full.FixStatus == "await_confirm" {
			return full, nil
		}
	}
	// 回落最近一条任务（完整行）。
	if len(tasks) > 0 {
		return s.Store.GetTask(ctx, tasks[0].ID)
	}
	return nil, nil
}

func fixPushPreview(s *Service, ctx context.Context, req *argus.FixPushPreviewRequest) (*argus.FixPushPreviewResponse, int) {
	if !fixPushAdmin(ctx) {
		fixPushDeny(s, ctx, "preview", req.GetPlatform()+":"+req.GetOwner()+"/"+req.GetRepo())
		return nil, http.StatusForbidden
	}
	platform, owner, repo, number := req.GetPlatform(), req.GetOwner(), req.GetRepo(), req.GetNumber()
	if platform == "" || owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		fixPushDeny(s, ctx, "preview.repo", platform+":"+owner+"/"+repo)
		return nil, http.StatusForbidden
	}
	resp := &argus.FixPushPreviewResponse{Message: "无任务"}
	if s.Cfg != nil && s.Cfg.FixPushMode != "" {
		resp.Mode = strPtr(s.Cfg.FixPushMode)
	}
	if !s.Cfg.FixPush {
		resp.Message = "修复推送环未启用（ARGUS_FIX_PUSH）"
		return resp, http.StatusOK
	}
	task, err := latestFixPushTask(s, ctx, platform, owner, repo, number)
	if err != nil || task == nil {
		if err != nil {
			s.log().Error("argus.fixpush.preview_failed", "error", err.Error())
			return nil, http.StatusInternalServerError
		}
		return resp, http.StatusOK
	}
	resp.TaskID = strPtr(task.ID)
	resp.HeadSha = strPtr(task.HeadSHA)
	resp.FixStatus = strPtr(task.FixStatus)

	// 待审批票数（当前 head）。
	if task.HeadSHA != "" {
		n, err := s.Store.CountPendingFixApprovals(ctx, platform, owner, repo, number, task.HeadSHA)
		if err != nil {
			return nil, http.StatusInternalServerError
		}
		resp.PendingApprovals = int32(n)
	}

	switch task.FixStatus {
	case "await_confirm":
		if resp.PendingApprovals == 0 && task.HeadSHA != "" {
			resp.Ready = true
			resp.Message = "就绪：可 POST /fix-push/confirm（绑定 expected_remote_head=" + task.HeadSHA + "）"
		} else {
			resp.Message = "待审批：高危票未全部批准（pending=" + itoa32(resp.PendingApprovals) + "）"
		}
	case "pushed", "pr_created", "cr_existed":
		resp.Message = "已推送（" + task.FixStatus + "）"
	case "":
		resp.Message = "任务未进入修复推送链"
	default:
		resp.Message = "fix_status=" + task.FixStatus
	}
	return resp, http.StatusOK
}

func fixPushConfirm(s *Service, ctx context.Context, req *argus.FixPushConfirmRequest) (*argus.FixPushConfirmResponse, int) {
	taskID := strings.TrimSpace(req.GetTaskID())
	expected := strings.TrimSpace(req.GetExpectedRemoteHead())
	if taskID == "" || expected == "" {
		return nil, http.StatusBadRequest
	}
	if !fixPushAdmin(ctx) {
		fixPushDeny(s, ctx, "confirm", taskID)
		return nil, http.StatusForbidden
	}
	task, err := s.Store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	// 仓库维度从任务记录解析（不信请求侧坐标）。
	if !s.Authorize(ctx, task.Platform, task.Owner, task.Repo) {
		fixPushDeny(s, ctx, "confirm.repo", task.Platform+":"+task.Owner+"/"+task.Repo)
		return nil, http.StatusForbidden
	}
	if s.FixPushConfirmFn == nil {
		return &argus.FixPushConfirmResponse{
			TaskID: taskID, Status: fixPushStatusRejected,
			Message: "confirm 执行体未装配（bootstrap 未注入）",
		}, http.StatusServiceUnavailable
	}
	if s.Cfg == nil || !s.Cfg.FixPush || s.Cfg.FixPushMode != "confirm" {
		return &argus.FixPushConfirmResponse{
			TaskID: taskID, Status: fixPushStatusRejected,
			Message: "当前 FixPushMode 非 confirm（需 ARGUS_FIX_PUSH=confirm）",
		}, http.StatusOK
	}
	status, message, headSHA, fixStatus, err := s.FixPushConfirmFn(ctx, taskID, expected)
	if err != nil {
		s.log().Error("argus.fixpush.confirm_failed", "task", taskID, "error", err.Error())
		return &argus.FixPushConfirmResponse{
			TaskID: taskID, Status: fixPushStatusFailed, Message: err.Error(),
		}, http.StatusOK
	}
	if s.Audit != nil {
		s.Audit.Log("fixpush.confirm", "task_id", taskID, "status", status,
			"token_fp", TokenFPFromContext(ctx))
	}
	resp := &argus.FixPushConfirmResponse{
		TaskID: taskID, Status: status, Message: message,
	}
	if headSHA != "" {
		resp.HeadSha = strPtr(headSHA)
	}
	if fixStatus != "" {
		resp.FixStatus = strPtr(fixStatus)
	}
	return resp, http.StatusOK
}

func itoa32(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
