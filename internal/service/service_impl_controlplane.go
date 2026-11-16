// service_impl_controlplane.go —— 控制面人工介入 API 的包级实现（转发壳见
// service_controlplane.go）。
//
// 权限纪律：每个方法入口先做 ScopeFromContext 二次判定（fail-closed）——路由
// 中间件已按 requiredScope("/controlplane/" → admin) 拦过一层，这里防 MCP/内部
// 直调绕过 HTTP 中间件。所有操作都是全局面（无仓库维度），故 routeAuthzMatrix
// 标 AuthzGateExempt 并在 note 写明控制面全局 admin。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// controlPlaneAdmin 控制面 scope 二次判定。返回 false 时调用方回 403。
func controlPlaneAdmin(ctx context.Context) bool { return ScopeFromContext(ctx) == "admin" }

// optionalString 取 optional string 的零值安全值。
func optionalString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// listUncertainEffects 的包级实现（转发壳见原文件）。
func listUncertainEffects(s *Service, ctx context.Context) (*argus.ControlPlaneListEffectsResponse, int) {
	if !controlPlaneAdmin(ctx) {
		return nil, http.StatusForbidden
	}
	// 控制面要看全部 uncertain，不设静置窗（#43 的 grace 语义只服务自动对账）；
	// cutoff 加 1s 防止时钟微差把刚转入 unknown 的行漏掉，不改变既有 API。
	rows, err := s.Store.SideEffects().ListUncertain(ctx, time.Now().UTC().Add(time.Second), 200)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	resp := &argus.ControlPlaneListEffectsResponse{
		Effects: make([]*argus.ControlPlaneEffect, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Effects = append(resp.Effects, &argus.ControlPlaneEffect{
			Key:         row.Key,
			Resource:    row.Resource,
			Reason:      row.Reason,
			RetrySafety: int32(row.RetrySafety),
			UpdatedAt:   row.UpdatedAt.Unix(),
		})
	}
	return resp, http.StatusOK
}

// resolveUncertainEffect 的包级实现（转发壳见原文件）。
func resolveUncertainEffect(s *Service, ctx context.Context, req *argus.ControlPlaneResolveRequest) (*argus.ControlPlaneResolveResponse, int) {
	if !controlPlaneAdmin(ctx) {
		return nil, http.StatusForbidden
	}
	if req == nil {
		return nil, http.StatusBadRequest
	}
	if req.Key == "" || len(req.Key) > 255 {
		return nil, http.StatusBadRequest
	}
	payload := optionalString(req.Payload)
	if payload != "" && !json.Valid([]byte(payload)) {
		return nil, http.StatusBadRequest
	}
	reason := optionalString(req.Reason)
	res := sideeffect.Resolution{Applied: req.Applied, ForceRetryable: req.ForceRetryable}
	if req.Applied {
		res.Result = sideeffect.Result{Payload: []byte(payload)}
		if reason == "" {
			reason = "manual resolve: applied"
		}
	} else if reason == "" {
		reason = "manual resolve: not applied"
	}
	res.Reason = reason

	outcome, out, err := s.Store.SideEffects().ResolveUncertainKey(ctx, req.Key, res)
	if err != nil {
		// 两种 409 都是「当前状态不接受这次裁决」的语义冲突，不是服务端故障：
		//   - ErrNotUncertain：Key 不在 uncertain（已 done/从未失败）；
		//   - ErrUnsafeNotRetryable：Unsafe 动作的「未生效」缺 force_retryable 签字。
		if errors.Is(err, sideeffect.ErrNotUncertain) || errors.Is(err, sideeffect.ErrUnsafeNotRetryable) {
			return nil, http.StatusConflict
		}
		return nil, http.StatusInternalServerError
	}
	resp := &argus.ControlPlaneResolveResponse{Outcome: outcome.String()}
	if len(out.Payload) > 0 {
		p := string(out.Payload)
		resp.Payload = &p
	}
	return resp, http.StatusOK
}

// listLeases 的包级实现（转发壳见原文件）。
func listLeases(s *Service, ctx context.Context) (*argus.ControlPlaneListLeasesResponse, int) {
	if !controlPlaneAdmin(ctx) {
		return nil, http.StatusForbidden
	}
	rows, err := s.Store.SideEffects().ListActiveLeases(ctx, 200)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	resp := &argus.ControlPlaneListLeasesResponse{
		Leases: make([]*argus.ControlPlaneLease, 0, len(rows)),
	}
	for _, row := range rows {
		var expiresAt int64
		if row.LeaseUntil != nil {
			expiresAt = row.LeaseUntil.Unix()
		}
		resp.Leases = append(resp.Leases, &argus.ControlPlaneLease{
			Resource:  row.Resource,
			Holder:    row.Holder,
			Fence:     row.Fence,
			ExpiresAt: expiresAt,
		})
	}
	return resp, http.StatusOK
}

// releaseLease 的包级实现（转发壳见原文件）。
func releaseLease(s *Service, ctx context.Context, req *argus.ControlPlaneReleaseLeaseRequest) (*argus.ControlPlaneReleaseLeaseResponse, int) {
	if !controlPlaneAdmin(ctx) {
		return nil, http.StatusForbidden
	}
	if req == nil || req.Resource == "" || len(req.Resource) > 255 {
		return nil, http.StatusBadRequest
	}
	cleared, err := s.Store.SideEffects().ForceReleaseResource(ctx, req.Resource)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	// 审计留痕：只记 token 指纹（脱敏），不记原 token。
	s.log().Info("argus.controlplane.lease_released", "resource", req.Resource,
		"cleared_actions", cleared, "token_fp", TokenFPFromContext(ctx),
		"reason", optionalString(req.Reason))
	return &argus.ControlPlaneReleaseLeaseResponse{ClearedActions: cleared}, http.StatusOK
}

// cancelTask 的包级实现（转发壳见原文件）。
func cancelTask(s *Service, ctx context.Context, req *argus.ControlPlaneCancelTaskRequest) (*argus.ControlPlaneCancelTaskResponse, int) {
	if !controlPlaneAdmin(ctx) {
		return nil, http.StatusForbidden
	}
	if req == nil || req.TaskID == "" {
		return nil, http.StatusBadRequest
	}
	result, err := s.Store.RequestTaskCancel(ctx, req.TaskID, optionalString(req.Reason))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		if errors.Is(err, storage.ErrTaskNotCancelable) {
			return nil, http.StatusConflict
		}
		return nil, http.StatusInternalServerError
	}
	// 审计留痕：token 指纹 + 理由（原 token 不落日志）。
	s.log().Info("argus.controlplane.task_cancelled", "task", req.TaskID,
		"cancelled", result.Cancelled, "token_fp", TokenFPFromContext(ctx),
		"reason", optionalString(req.Reason))
	// SSE 观测面补终态事件：控制面绕过了 runner 的 finishTask 漏斗，不补则
	// /events 客户端看不到任务已结束。取任务坐标失败只告警（状态已写入）。
	if s.Bus != nil {
		if t, gerr := s.Store.GetTask(ctx, req.TaskID); gerr == nil {
			s.publishProgress(progress.Event{
				Type: progress.EventTaskFinished, TaskID: t.ID, Platform: t.Platform,
				Owner: t.Owner, Repo: t.Repo, Number: t.Number, Status: result.Status,
			})
		} else {
			s.log().Warn("argus.controlplane.cancel_event_skip", "task", req.TaskID, "error", gerr.Error())
		}
	}
	return &argus.ControlPlaneCancelTaskResponse{
		TaskID:    req.TaskID,
		Status:    result.Status,
		Cancelled: result.Cancelled,
	}, http.StatusOK
}
