// service_impl_controlplane.go —— 控制面人工介入 API 的包级实现（转发壳见
// service_controlplane.go）。
//
// 权限纪律（#60 两级语义）：每个方法入口先做 ScopeFromContext 二次判定
// （fail-closed，防绕过 HTTP 中间件的内部直调），再按仓库约束分级——
// 无约束 admin = system_admin（全局）；带 Repos 约束的 admin = repo_admin
// （仅约束内仓库可见/可操作，与 repo-settings「约束内 admin 可写」口径一致）。
// 资源身份从**库记录**解析（effect.ParseResourceRepo：pr:/branch: 形态），
// 不可解析资源仅 system_admin 可见——repo_admin 的授权半径必须能指到具体
// 仓库，指不到就不给。routeAuthzMatrix 仍标 AuthzGateExempt：仓库维度在
// 方法内收口（列表过滤 + 单资源 403），拒绝留 token 指纹审计。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// controlPlaneAdmin 控制面 scope 二次判定。返回 false 时调用方回 403。
func controlPlaneAdmin(ctx context.Context) bool { return ScopeFromContext(ctx) == "admin" }

// controlPlaneRepoAllowed 控制面仓库维度判定（#60）：先过 scope 门，再分级——
// 无仓库约束（system_admin）全局放行；带约束（repo_admin）按资源身份
// （platform/owner/repo）对 pattern 判定。platform 为空 = 资源身份不可解析，
// 仅 system_admin 放行（fail-closed）。
func controlPlaneRepoAllowed(ctx context.Context, platform, owner, repo string) bool {
	if !controlPlaneAdmin(ctx) {
		return false
	}
	tp := TokenReposFromContext(ctx)
	if tp == "" {
		return true
	}
	if platform == "" {
		return false
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}

// controlPlaneDeny 拒绝留痕（#60）：token 指纹 + 动作 + 对象，不落原 token；
// 拒绝计数走 authzDeniedMetric（route×scope 维度可观测）。
func controlPlaneDeny(s *Service, ctx context.Context, action, subject string) {
	authzDeniedMetric(ctx, "controlplane."+action)
	s.log().Warn("argus.controlplane.repo_admin_denied", "action", action,
		"subject", subject, "token_fp", TokenFPFromContext(ctx))
}

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
	rows, err := s.Store.SideEffectAdmin().ListUncertain(ctx, time.Now().UTC().Add(time.Second), 200)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	resp := &argus.ControlPlaneListEffectsResponse{
		Effects: make([]*argus.ControlPlaneEffect, 0, len(rows)),
	}
	for _, row := range rows {
		p, o, rr, _ := effect.ParseResourceRepo(row.Resource)
		if !controlPlaneRepoAllowed(ctx, p, o, rr) {
			continue // repo_admin：约束外/不可归属资源的行不可见
		}
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
	// #60 裁决前授权：从库记录解析资源身份（不信请求侧拼装），repo_admin
	// 约束外/不可归属 → 403。Key 不存在落到 ResolveUncertainKey 的既有 409
	// 契约（不因授权查询泄露 Key 存在性差异）。
	if row, gerr := s.Store.SideEffectAdmin().GetEffectByKeyForAuthz(ctx, req.Key); gerr == nil {
		p, o, rr, _ := effect.ParseResourceRepo(row.Resource)
		if !controlPlaneRepoAllowed(ctx, p, o, rr) {
			controlPlaneDeny(s, ctx, "resolve", req.Key)
			return nil, http.StatusForbidden
		}
	} else if !errors.Is(gerr, storage.ErrNotFound) {
		return nil, http.StatusInternalServerError
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

	outcome, out, err := s.Store.SideEffectAdmin().ResolveUncertainKey(ctx, req.Key, res)
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
	rows, err := s.Store.SideEffectAdmin().ListActiveLeases(ctx, 200)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	resp := &argus.ControlPlaneListLeasesResponse{
		Leases: make([]*argus.ControlPlaneLease, 0, len(rows)),
	}
	for _, row := range rows {
		p, o, rr, _ := effect.ParseResourceRepo(row.Resource)
		if !controlPlaneRepoAllowed(ctx, p, o, rr) {
			continue // repo_admin：约束外/不可归属资源的租约不可见
		}
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
	// #60 强释前授权：归属判定用库记录里的资源串（请求的 resource 只作查询键），
	// repo_admin 约束外/不可归属 → 403。资源行不存在由 ForceRelease 判 404。
	if row, gerr := s.Store.SideEffectAdmin().GetLeaseByResourceForAuthz(ctx, req.Resource); gerr == nil {
		p, o, rr, _ := effect.ParseResourceRepo(row.Resource)
		if !controlPlaneRepoAllowed(ctx, p, o, rr) {
			controlPlaneDeny(s, ctx, "release_lease", req.Resource)
			return nil, http.StatusForbidden
		}
	} else if !errors.Is(gerr, storage.ErrNotFound) {
		return nil, http.StatusInternalServerError
	}
	cleared, err := s.Store.SideEffectAdmin().ForceReleaseResource(ctx, req.Resource)
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
	// #60 取消前授权：先加载任务，按 task.Platform/Owner/Repo 走 AuthorizeTask
	// （repo_admin 仅约束内可取消；platform=local 与数据面 taskVisible 口径一致）。
	t, gerr := s.Store.GetTask(ctx, req.TaskID)
	if gerr != nil {
		if errors.Is(gerr, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.AuthorizeTask(ctx, t.Platform, t.Owner, t.Repo) {
		controlPlaneDeny(s, ctx, "cancel_task", req.TaskID)
		return nil, http.StatusForbidden
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
	// /events 客户端看不到任务已结束。任务已在上面加载，直接复用坐标。
	if s.Bus != nil {
		s.publishProgress(progress.Event{
			Type: progress.EventTaskFinished, TaskID: t.ID, Platform: t.Platform,
			Owner: t.Owner, Repo: t.Repo, Number: t.Number, Status: result.Status,
		})
	}
	return &argus.ControlPlaneCancelTaskResponse{
		TaskID:    req.TaskID,
		Status:    result.Status,
		Cancelled: result.Cancelled,
	}, http.StatusOK
}
