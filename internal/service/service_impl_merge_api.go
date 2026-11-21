// service_impl_merge_api.go —— 受控合并 HTTP 面（eligibility/preview/confirm/
// get/reconcile）。资格门判定见 service_impl_merge.go。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// ---------- HTTP 面 ----------

func mergeEligibility(s *Service, ctx context.Context, req *argus.MergeEligibilityRequest) (*argus.MergeEligibilityResponse, int) {
	if !mergeAdmin(ctx) {
		mergeDeny(s, ctx, "eligibility", req.GetPlatform()+":"+req.GetOwner()+"/"+req.GetRepo())
		return nil, http.StatusForbidden
	}
	platform, owner, repo, number := req.GetPlatform(), req.GetOwner(), req.GetRepo(), req.GetNumber()
	if platform == "" || owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		mergeDeny(s, ctx, "eligibility.repo", platform+":"+owner+"/"+repo)
		return nil, http.StatusForbidden
	}
	g, err := evaluateMergeGate(s, ctx, platform, owner, repo, number, "")
	if err != nil {
		s.log().Error("argus.merge.eligibility_failed", "error", err.Error())
		return nil, http.StatusBadGateway
	}
	return &argus.MergeEligibilityResponse{Eligibility: toIDLGate(g)}, http.StatusOK
}

func mergePreview(s *Service, ctx context.Context, req *argus.MergePreviewRequest) (*argus.MergePreviewResponse, int) {
	if !mergeAdmin(ctx) {
		mergeDeny(s, ctx, "preview", req.GetPlatform()+":"+req.GetOwner()+"/"+req.GetRepo())
		return nil, http.StatusForbidden
	}
	platform, owner, repo, number := req.GetPlatform(), req.GetOwner(), req.GetRepo(), req.GetNumber()
	if platform == "" || owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		mergeDeny(s, ctx, "preview.repo", platform+":"+owner+"/"+repo)
		return nil, http.StatusForbidden
	}
	g, err := evaluateMergeGate(s, ctx, platform, owner, repo, number, "")
	if err != nil {
		s.log().Error("argus.merge.preview_gate_failed", "error", err.Error())
		return nil, http.StatusBadGateway
	}
	resp := &argus.MergePreviewResponse{
		Eligible:           g.Eligible,
		ExpectedRemoteHead: g.HeadSHA,
		Eligibility:        toIDLGate(g),
		RejectCode:         g.firstFailCode(),
	}
	if !g.Eligible || g.HeadSHA == "" {
		return resp, http.StatusOK // 预检失败是业务结果，不是 HTTP 错误
	}
	gateJSON, _ := json.Marshal(g)
	op := &storage.MergeOp{
		Platform:           platform,
		Owner:              owner,
		Repo:               repo,
		Number:             number,
		ExpectedRemoteHead: g.HeadSHA,
		Status:             storage.MergeOpPending,
		GateJSON:           string(gateJSON),
	}
	if err := s.Store.CreateMergeOp(ctx, op); err != nil {
		s.log().Error("argus.merge.preview_persist_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	resp.OpID = op.ID
	return resp, http.StatusOK
}

func mergeConfirm(s *Service, ctx context.Context, req *argus.MergeConfirmRequest) (*argus.MergeConfirmResponse, int) {
	opID := strings.TrimSpace(req.GetOpID())
	expected := strings.TrimSpace(req.GetExpectedRemoteHead())
	if opID == "" || expected == "" {
		return nil, http.StatusBadRequest
	}
	if !mergeAdmin(ctx) {
		mergeDeny(s, ctx, "confirm", opID)
		return nil, http.StatusForbidden
	}
	op, err := s.Store.GetMergeOp(ctx, opID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	// 仓库维度：从 op 记录解析，不信请求侧坐标。
	if !s.Authorize(ctx, op.Platform, op.Owner, op.Repo) {
		mergeDeny(s, ctx, "confirm.repo", op.Platform+":"+op.Owner+"/"+op.Repo)
		return nil, http.StatusForbidden
	}
	// 绑定：请求 expected 必须等于 preview 绑定。
	if !strings.EqualFold(op.ExpectedRemoteHead, expected) {
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpRejected,
			Message: strPtr("expected_remote_head 与候选绑定不一致"),
		}, http.StatusOK
	}
	// 终态幂等：已 merged 直接回放；其余终态拒绝。
	switch op.Status {
	case storage.MergeOpMerged:
		return &argus.MergeConfirmResponse{OpID: opID, Status: storage.MergeOpMerged, Message: strPtr("already merged")}, http.StatusOK
	case storage.MergeOpFailed, storage.MergeOpExpired, storage.MergeOpRejected:
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpRejected, Message: strPtr("候选已终态: " + op.Status),
		}, http.StatusOK
	case storage.MergeOpUncertain:
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpUncertain, Message: strPtr("结果未知，请先 reconcile"),
		}, http.StatusOK
	}

	// 重跑资格门 + 绑定远端当前 head（confirm 时刻的 TOCTOU 防线）。
	g, gerr := evaluateMergeGate(s, ctx, op.Platform, op.Owner, op.Repo, op.Number, op.ExpectedRemoteHead)
	if gerr != nil {
		s.log().Error("argus.merge.confirm_gate_failed", "error", gerr.Error())
		return nil, http.StatusBadGateway
	}
	if !g.Eligible {
		// 门失败：候选标 rejected（不再可 confirm），透出机器码。
		_, _ = s.Store.TransitionMergeOp(ctx, opID, storage.MergeOpPending, storage.MergeOpRejected,
			"gate:"+g.firstFailCode(), "")
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpRejected,
			Message: strPtr("资格门未通过: " + g.firstFailCode()),
		}, http.StatusOK
	}

	// 副作用闸：Prepare = 再读远端核对 head；Apply = MergeCR。
	// RetrySafetyUnsafe：合并不可安全重试，未知态必须先对账。
	effKey := storage.MergeOpEffectKey(opID)
	resource := effect.EncodeMergeResource(tenant.FromContext(ctx), op.Platform, op.Owner, op.Repo, op.Number)
	act := effect.Action{
		Key:         effect.Key(effKey),
		Resource:    resource,
		Payload:     []byte(opID),
		RetrySafety: effect.RetrySafetyUnsafe,
	}
	runner := effect.NewFromStore(s.Store, effect.PushTTL, nil, effect.Enabled())
	outcome, result, doErr := runner.DoPrepared(ctx, act,
		func(ctx context.Context) (any, error) {
			cr, err := s.SDK.GetCR(ctx, op.Platform, op.Owner, op.Repo, op.Number)
			if err != nil {
				return nil, err
			}
			if cr == nil || cr.State != model.CRStateOpened {
				return nil, errors.New("PR 已非 opened")
			}
			if !strings.EqualFold(cr.HeadSHA, op.ExpectedRemoteHead) {
				return nil, errors.New("远端 head 已变化")
			}
			return cr.HeadSHA, nil
		},
		func(ctx context.Context, prepared any, lease effect.Lease) (effect.Result, error) {
			cr, err := s.SDK.MergeCR(ctx, op.Platform, op.Owner, op.Repo, op.Number, provider.MergeCROptions{})
			if err != nil {
				return effect.Result{}, err
			}
			payload, _ := json.Marshal(map[string]any{
				"state": string(cr.State), "head_sha": cr.HeadSHA,
			})
			return effect.Result{Payload: payload}, nil
		},
	)

	switch {
	case errors.Is(doErr, effect.ErrOutcomeUnknown):
		_, _ = s.Store.TransitionMergeOp(ctx, opID, storage.MergeOpPending, storage.MergeOpUncertain,
			"merge apply uncertain", "")
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpUncertain,
			Message: strPtr("远端结果未知，请 POST /merges/reconcile 读回对账"),
		}, http.StatusOK
	case errors.Is(doErr, effect.ErrContended):
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpRejected, Message: strPtr("副作用闸占用，请稍后重试"),
		}, http.StatusConflict
	case doErr != nil:
		_, _ = s.Store.TransitionMergeOp(ctx, opID, storage.MergeOpPending, storage.MergeOpFailed,
			doErr.Error(), "")
		return &argus.MergeConfirmResponse{
			OpID: opID, Status: storage.MergeOpFailed, Message: strPtr(doErr.Error()),
		}, http.StatusOK
	}

	// Applied 或 Replayed → merged。
	statusText := storage.MergeOpMerged
	if outcome == effect.OutcomeReplayed {
		statusText = "merged"
	}
	resultJSON := string(result.Payload)
	_, _ = s.Store.TransitionMergeOp(ctx, opID, storage.MergeOpPending, storage.MergeOpMerged,
		"merge applied", resultJSON)
	// 审计：合并是最高危写，必留痕。
	if s.Audit != nil {
		s.Audit.Log("merge.confirm", "op_id", opID,
			"repo", op.Platform+":"+op.Owner+"/"+op.Repo, "head", op.ExpectedRemoteHead,
			"token_fp", TokenFPFromContext(ctx))
	}
	mergedSHA := ""
	var parsed map[string]any
	if json.Unmarshal([]byte(resultJSON), &parsed) == nil {
		if hs, ok := parsed["head_sha"].(string); ok {
			mergedSHA = hs
		}
	}
	return &argus.MergeConfirmResponse{
		OpID: opID, Status: statusText, MergedSha: strPtr(mergedSHA),
		Message: strPtr("merge applied"),
	}, http.StatusOK
}

func mergeGet(s *Service, ctx context.Context, opID string) (*argus.MergeGetResponse, int) {
	if !mergeAdmin(ctx) {
		mergeDeny(s, ctx, "get", opID)
		return nil, http.StatusForbidden
	}
	op, err := s.Store.GetMergeOp(ctx, strings.TrimSpace(opID))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.Authorize(ctx, op.Platform, op.Owner, op.Repo) {
		mergeDeny(s, ctx, "get.repo", op.Platform+":"+op.Owner+"/"+op.Repo)
		return nil, http.StatusForbidden
	}
	return &argus.MergeGetResponse{Op: toIDLOp(op)}, http.StatusOK
}

func mergeReconcile(s *Service, ctx context.Context, req *argus.MergeReconcileRequest) (*argus.MergeReconcileResponse, int) {
	opID := strings.TrimSpace(req.GetOpID())
	if opID == "" {
		return nil, http.StatusBadRequest
	}
	if !mergeAdmin(ctx) {
		mergeDeny(s, ctx, "reconcile", opID)
		return nil, http.StatusForbidden
	}
	op, err := s.Store.GetMergeOp(ctx, opID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.Authorize(ctx, op.Platform, op.Owner, op.Repo) {
		mergeDeny(s, ctx, "reconcile.repo", op.Platform+":"+op.Owner+"/"+op.Repo)
		return nil, http.StatusForbidden
	}
	if op.Status != storage.MergeOpUncertain && op.Status != storage.MergeOpPending {
		return &argus.MergeReconcileResponse{
			Status: op.Status, Message: strPtr("非 uncertain/pending，无需对账"),
		}, http.StatusOK
	}

	cr, gerr := s.SDK.GetCR(ctx, op.Platform, op.Owner, op.Repo, op.Number)
	if gerr != nil {
		return nil, http.StatusBadGateway
	}
	if cr == nil {
		return nil, http.StatusBadGateway
	}
	switch cr.State {
	case model.CRStateMerged:
		payload, _ := json.Marshal(map[string]any{"state": "merged", "head_sha": cr.HeadSHA})
		// pending → merged（confirm 已发出但响应丢失）或 uncertain → merged。
		from := op.Status
		if from == storage.MergeOpPending || from == storage.MergeOpUncertain {
			_, _ = s.Store.TransitionMergeOp(ctx, opID, from, storage.MergeOpMerged,
				"reconcile: merged on platform", string(payload))
		}
		return &argus.MergeReconcileResponse{Status: storage.MergeOpMerged,
			Message: strPtr("平台已合并")}, http.StatusOK
	case model.CRStateOpened:
		// 仍未合并：uncertain → failed（可重新 preview；不自动重试 Unsafe）。
		if op.Status == storage.MergeOpUncertain {
			_, _ = s.Store.TransitionMergeOp(ctx, opID, storage.MergeOpUncertain, storage.MergeOpFailed,
				"reconcile: still open", "")
		}
		return &argus.MergeReconcileResponse{Status: op.Status,
			Message: strPtr("平台仍为 opened（未合并）")}, http.StatusOK
	default:
		if op.Status == storage.MergeOpUncertain || op.Status == storage.MergeOpPending {
			_, _ = s.Store.TransitionMergeOp(ctx, opID, op.Status, storage.MergeOpFailed,
				"reconcile: state="+string(cr.State), "")
		}
		return &argus.MergeReconcileResponse{Status: storage.MergeOpFailed,
			Message: strPtr("平台状态=" + string(cr.State))}, http.StatusOK
	}
}

func toIDLOp(op *storage.MergeOp) *argus.MergeOperation {
	out := &argus.MergeOperation{
		OpID:               op.ID,
		Platform:           op.Platform,
		Owner:              op.Owner,
		Repo:               op.Repo,
		Number:             op.Number,
		Status:             op.Status,
		ExpectedRemoteHead: op.ExpectedRemoteHead,
		Reason:             strPtr(op.Reason),
		ResultJSON:         strPtr(op.ResultJSON),
		CreatedAt:          op.CreatedAt.Unix(),
		UpdatedAt:          op.UpdatedAt.Unix(),
	}
	return out
}

// ensure time import used (ConfirmedAt etc. not always referenced).
var _ = time.Now
