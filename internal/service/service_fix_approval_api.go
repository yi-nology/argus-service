// service_fix_approval_api.go —— 修复审批票 HTTP 面（list/decide，admin 档）。
package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// ListFixApprovals 按 PR 列票（GET /fix-approvals）：懒 Ensure 当前 head 高危票。
func (s *Service) ListFixApprovals(ctx context.Context, req *argus.FixApprovalListRequest) (*argus.FixApprovalListResponse, int) {
	return listFixApprovals(s, ctx, req)
}

// DecideFixApproval 裁决 pending 票（POST /fix-approvals/decide）。
func (s *Service) DecideFixApproval(ctx context.Context, req *argus.FixApprovalDecideRequest) (*argus.FixApprovalDecideResponse, int) {
	return decideFixApproval(s, ctx, req)
}

func fixApprovalAdmin(ctx context.Context) bool { return ScopeFromContext(ctx) == "admin" }

func fixApprovalDeny(s *Service, ctx context.Context, action, subject string) {
	authzDeniedMetric(ctx, "fixapproval."+action)
	s.log().Warn("argus.fixapproval.denied", "action", action, "subject", subject,
		"token_fp", TokenFPFromContext(ctx))
}

func listFixApprovals(s *Service, ctx context.Context, req *argus.FixApprovalListRequest) (*argus.FixApprovalListResponse, int) {
	if !fixApprovalAdmin(ctx) {
		fixApprovalDeny(s, ctx, "list", req.GetPlatform()+":"+req.GetOwner()+"/"+req.GetRepo())
		return nil, http.StatusForbidden
	}
	platform, owner, repo, number := req.GetPlatform(), req.GetOwner(), req.GetRepo(), req.GetNumber()
	if platform == "" || owner == "" || repo == "" || number == "" {
		return nil, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		fixApprovalDeny(s, ctx, "list.repo", platform+":"+owner+"/"+repo)
		return nil, http.StatusForbidden
	}
	head := req.GetHeadSha()
	if head == "" {
		head = currentHeadSHA(s, ctx, platform, owner, repo, number)
	}
	if head != "" {
		if _, err := ensureFixApprovalsForPR(s, ctx, platform, owner, repo, number, head, ""); err != nil {
			s.log().Error("argus.fixapproval.ensure_failed", "error", err.Error())
			return nil, http.StatusInternalServerError
		}
	}
	rows, err := s.Store.ListFixApprovals(ctx, platform, owner, repo, number, head)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	resp := &argus.FixApprovalListResponse{HeadSha: strPtr(head)}
	for i := range rows {
		resp.Tickets = append(resp.Tickets, toIDLFixTicket(&rows[i]))
	}
	if head != "" {
		n, err := s.Store.CountPendingFixApprovals(ctx, platform, owner, repo, number, head)
		if err != nil {
			return nil, http.StatusInternalServerError
		}
		resp.PendingCount = n
	}
	return resp, http.StatusOK
}

func decideFixApproval(s *Service, ctx context.Context, req *argus.FixApprovalDecideRequest) (*argus.FixApprovalDecideResponse, int) {
	id := strings.TrimSpace(req.GetID())
	decision := strings.TrimSpace(req.GetDecision())
	if id == "" {
		return nil, http.StatusBadRequest
	}
	if decision != storage.FixApprovalApproved && decision != storage.FixApprovalRejected {
		return nil, http.StatusBadRequest
	}
	if !fixApprovalAdmin(ctx) {
		fixApprovalDeny(s, ctx, "decide", id)
		return nil, http.StatusForbidden
	}
	t, err := s.Store.GetFixApproval(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, http.StatusNotFound
		}
		return nil, http.StatusInternalServerError
	}
	if !s.Authorize(ctx, t.Platform, t.Owner, t.Repo) {
		fixApprovalDeny(s, ctx, "decide.repo", t.Platform+":"+t.Owner+"/"+t.Repo)
		return nil, http.StatusForbidden
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	changed, err := s.Store.DecideFixApproval(ctx, id, decision, TokenFPFromContext(ctx), reason)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	// 重读最终状态。
	fresh, _ := s.Store.GetFixApproval(ctx, id)
	status := decision
	msg := strPtr("ok")
	if !changed {
		status = t.Status
		msg = strPtr("票非 pending，幂等拒绝（当前状态=" + t.Status + "）")
	}
	if s.Audit != nil {
		s.Audit.Log("fixapproval.decide", "id", id, "decision", decision,
			"changed", changed, "token_fp", TokenFPFromContext(ctx),
			"repo", t.Platform+":"+t.Owner+"/"+t.Repo)
	}
	if fresh != nil {
		status = fresh.Status
	}
	return &argus.FixApprovalDecideResponse{
		ID: id, Status: status, Changed: changed, Message: msg,
	}, http.StatusOK
}

func toIDLFixTicket(t *storage.FixApprovalTicket) *argus.FixApprovalTicket {
	out := &argus.FixApprovalTicket{
		ID: t.ID, Platform: t.Platform, Owner: t.Owner, Repo: t.Repo, Number: t.Number,
		HeadSha: t.HeadSHA, Print: t.Print, File: t.File, Severity: t.Severity,
		Status: t.Status, CreatedAt: t.CreatedAt.Unix(),
	}
	if t.Reason != "" {
		out.Reason = strPtr(t.Reason)
	}
	if t.DecidedByFP != "" {
		out.DecidedByFp = strPtr(t.DecidedByFP)
	}
	if t.DecidedAt != nil {
		v := t.DecidedAt.Unix()
		out.DecidedAt = &v
	}
	return out
}
