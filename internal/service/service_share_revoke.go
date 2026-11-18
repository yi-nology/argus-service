// service_share_revoke.go —— share 令牌撤销控制面（#91）。

package service

import (
	"context"
	"net/http"
	"strings"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// RevokeShare POST /controlplane/shares/revoke：撤销 share 令牌。
// subject = 任务 ID（影响面域）或 "platform:owner/repo"（repo-map 域）。
// admin 档由 /controlplane 前缀的 requiredScope 收口；幂等（重复撤销同 subject 原地刷新）。
func (s *Service) RevokeShare(ctx context.Context, req *argus.ControlPlaneRevokeShareRequest) (*argus.ControlPlaneRevokeShareResponse, int) {
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		return nil, http.StatusBadRequest
	}
	if s.Store == nil {
		return nil, http.StatusServiceUnavailable
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	if _, err := s.Store.RevokeShare(ctx, subject, reason, TokenFPFromContext(ctx)); err != nil {
		s.log().Error("argus.share.revoke_failed", "subject", subject, "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	s.Audit.Log("share.revoked", "subject", subject)
	return &argus.ControlPlaneRevokeShareResponse{Revoked: true}, http.StatusOK
}
