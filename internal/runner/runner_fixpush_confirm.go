// runner_fixpush_confirm.go —— T4 用户面 confirm 推送：操作员绑定
// expected_remote_head 后放行 await_confirm 任务的修复推送。
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// FixPushConfirmResult confirm 的结构化结果（service 层映射 HTTP）。
type FixPushConfirmResult struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"` // pushed / rejected / failed
	Message  string `json:"message"`
	HeadSHA  string `json:"head_sha"`
	FixStatu string `json:"fix_status"`
}

// ConfirmFixPush 放行一次 await_confirm 推送：
//  1. 任务须存在且 FixStatus=await_confirm（幂等：已 push/reject 不重入）；
//  2. expectedHead 必须等于任务 head（陈旧绑定拒绝）；
//  3. 远端 PR head 复核一致（TOCTOU）；
//  4. 重建 PRContext + 从 artifacts 取 findings，走既有 pushVerifiedFixes
//     （内含审批门复查 + effect at-most-once）。
func (r *Runner) ConfirmFixPush(ctx context.Context, taskID, expectedHead string) (*FixPushConfirmResult, error) {
	return confirmFixPush(r, ctx, taskID, expectedHead)
}

func confirmFixPush(r *Runner, ctx context.Context, taskID, expectedHead string) (*FixPushConfirmResult, error) {
	if r == nil || r.Store == nil {
		return nil, errors.New("runner 未装配")
	}
	taskID = strings.TrimSpace(taskID)
	expectedHead = strings.TrimSpace(expectedHead)
	if taskID == "" || expectedHead == "" {
		return nil, errors.New("task_id 与 expected_remote_head 必填")
	}
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	res := &FixPushConfirmResult{TaskID: task.ID, HeadSHA: task.HeadSHA, FixStatu: task.FixStatus}

	// 状态机：只有 await_confirm 可 confirm。
	switch task.FixStatus {
	case "await_confirm":
		// ok
	case "pushed", "pr_created", "cr_existed":
		res.Status = "rejected"
		res.Message = "已推送过（幂等）: " + task.FixStatus
		return res, nil
	default:
		res.Status = "rejected"
		res.Message = "非 await_confirm 状态: " + orEmpty(task.FixStatus)
		return res, nil
	}

	// 绑定：请求 expected 必须等于任务 head。
	if !strings.EqualFold(task.HeadSHA, expectedHead) {
		res.Status = "rejected"
		res.Message = "expected_remote_head 与任务 head 不一致"
		return res, nil
	}

	// 远端复核（TOCTOU）。
	pr, err := buildPRContext(r, ctx, task)
	if err != nil {
		return nil, fmt.Errorf("构建 PR 上下文失败: %w", err)
	}
	if !strings.EqualFold(pr.HeadSHA, expectedHead) {
		res.Status = "rejected"
		res.Message = "远端 head 已变化（陈旧绑定）"
		return res, nil
	}
	res.HeadSHA = pr.HeadSHA

	// 取 staged findings。
	raw := storage.DecodeJSONMap(task.ArtifactsJSON)[ArtifactFixPushFindings]
	if raw == "" {
		res.Status = "rejected"
		res.Message = "缺少 staged findings（artifacts.fix_push_findings）"
		return res, nil
	}
	var staged struct {
		Findings []domain.Finding `json:"findings"`
		Verified int              `json:"verified"`
	}
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return nil, fmt.Errorf("staged findings 解析失败: %w", err)
	}
	if len(staged.Findings) == 0 {
		res.Status = "rejected"
		res.Message = "staged findings 为空"
		return res, nil
	}

	// 审批门复查（confirm 时刻 fail-closed）。
	if !fixApprovalsReady(r, ctx, task, pr, staged.Findings) {
		res.Status = "rejected"
		res.Message = "高危修复审批未通过"
		return res, nil
	}

	// 工作副本 + 推送。
	key := plugin.PRToKey(*pr)
	dir, err := r.wcFor(ctx).Ensure(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("工作副本不可用: %w", err)
	}
	defer r.wcFor(ctx).Release(key)

	// pushVerifiedFixes 内部自落 FixStatus；confirm 后状态以任务为准。
	pushVerifiedFixes(r, ctx, task, pr, dir, staged.Findings, staged.Verified)

	// 重读终态。
	if fresh, err := r.Store.GetTask(ctx, task.ID); err == nil {
		res.FixStatu = fresh.FixStatus
	}
	switch res.FixStatu {
	case "pushed", "pr_created", "cr_existed":
		res.Status = "pushed"
		res.Message = "推送完成"
	case "pushing":
		// 同步路径下不应停留；兜底。
		res.Status = "pushed"
		res.Message = "推送已受理"
	default:
		res.Status = fixResultFailed
		res.Message = "推送未成功（fix_status=" + orEmpty(res.FixStatu) + "）"
	}
	return res, nil
}

func orEmpty(s string) string {
	if s == "" {
		return "(空)"
	}
	return s
}
