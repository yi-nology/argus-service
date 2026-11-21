// service_auth_user.go —— 多用户登录/邀请/权限投影（T5）。
package service

import (
	"context"
	"net/http"
	"strings"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/storage"
)

// sessionTTL 会话有效期（24h；轮换/撤销走 logout）。
const sessionTTL = 24 * time.Hour

// inviteTTL 缺省邀请有效期。
const inviteTTL = 72 * time.Hour

// AuthLogin 用户名密码登录（POST /auth/login，公开）。
func (s *Service) AuthLogin(ctx context.Context, req *argus.AuthLoginRequest) (*argus.AuthLoginResponse, int) {
	return authLogin(s, ctx, req)
}

// AuthLogout 登出（POST /auth/logout，需登录）。
func (s *Service) AuthLogout(ctx context.Context) (*argus.AuthLogoutResponse, int) {
	return authLogout(s, ctx)
}

// AuthMe 当前身份 + 权限投影（GET /auth/me，需登录）。
func (s *Service) AuthMe(ctx context.Context) (*argus.AuthMeResponse, int) {
	return authMe(s, ctx)
}

// AuthInvite 创建邀请（POST /auth/invites，admin）。
func (s *Service) AuthInvite(ctx context.Context, req *argus.AuthInviteRequest) (*argus.AuthInviteResponse, int) {
	return authInvite(s, ctx, req)
}

// AuthAcceptInvite 接受邀请（POST /auth/accept-invite，公开）。
func (s *Service) AuthAcceptInvite(ctx context.Context, req *argus.AuthAcceptInviteRequest) (*argus.AuthAcceptInviteResponse, int) {
	return authAcceptInvite(s, ctx, req)
}

func authLogin(s *Service, ctx context.Context, req *argus.AuthLoginRequest) (*argus.AuthLoginResponse, int) {
	if s.Store == nil {
		return nil, http.StatusServiceUnavailable
	}
	username := strings.TrimSpace(req.GetUsername())
	password := req.GetPassword()
	if username == "" || password == "" {
		return nil, http.StatusBadRequest
	}
	u, err := s.Store.Auths().VerifyLogin(ctx, username, password)
	if err != nil {
		s.log().Warn("argus.auth.login_failed", "username", username, "error", err.Error())
		// 不区分「用户不存在/密码错」（防用户名枚举）。
		return nil, http.StatusUnauthorized
	}
	token, sess, err := s.Store.Auths().CreateSession(ctx, u.ID, u.Role, sessionTTL)
	if err != nil {
		s.log().Error("argus.auth.session_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	if s.Audit != nil {
		s.Audit.Log("auth.login", "username", u.Username, "role", u.Role)
	}
	return &argus.AuthLoginResponse{
		Token: token, Username: u.Username, Role: u.Role,
		Scope: storage.RoleToScope(u.Role), ExpiresAt: sess.ExpiresAt.Unix(),
	}, http.StatusOK
}

func authLogout(s *Service, ctx context.Context) (*argus.AuthLogoutResponse, int) {
	auth := bearerRaw(ctx) // 从 ctx 取不到 raw——改从 handler 传？见 authLogoutWithToken
	// 实际 raw token 由 handler 注入 ctx。
	raw := TokenRawFromContext(ctx)
	if raw == "" || s.Store == nil {
		return &argus.AuthLogoutResponse{Revoked: false}, http.StatusOK
	}
	if err := s.Store.Auths().RevokeSession(ctx, raw); err != nil {
		return nil, http.StatusInternalServerError
	}
	_ = auth
	return &argus.AuthLogoutResponse{Revoked: true}, http.StatusOK
}

// tokenRawKey raw bearer 的 ctx 键（logout 用；登录响应 token 不回读）。
type tokenRawKey struct{}

// ContextWithTokenRaw 写入原始 Bearer token（handler 在 logout 前注入）。
func ContextWithTokenRaw(ctx context.Context, raw string) context.Context {
	return context.WithValue(ctx, tokenRawKey{}, raw)
}

// TokenRawFromContext 取原始 Bearer token。
func TokenRawFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tokenRawKey{}).(string)
	return s
}

func bearerRaw(ctx context.Context) string { return TokenRawFromContext(ctx) }

func authMe(s *Service, ctx context.Context) (*argus.AuthMeResponse, int) {
	username, role, ok := SessionFromContext(ctx)
	if !ok {
		// 静态 token：投影 scope 而非用户名。
		scope := ScopeFromContext(ctx)
		if scope == "" {
			return nil, http.StatusUnauthorized
		}
		return &argus.AuthMeResponse{
			Username: "(static-token)", Role: "token", Scope: scope,
			Permissions: permissionsForScope(scope, ""),
		}, http.StatusOK
	}
	return &argus.AuthMeResponse{
		Username: username, Role: role, Scope: storage.RoleToScope(role),
		Permissions: permissionsForScope(storage.RoleToScope(role), role),
	}, http.StatusOK
}

// permissionsForScope my_permissions 投影（MergePilot 同名能力的最小对齐）。
// decide/apply/merge 绑 admin 档（与 requiredScope 一致）；manage_* 绑 admin。
func permissionsForScope(scope, role string) *argus.AuthPermissions {
	isAdmin := scope == "admin"
	isCI := isAdmin || scope == "ci"
	isRead := isCI || scope == "read"
	// auditor 角色：只读 + 审计（简化：read；审计面本就在 admin——如实投影 false）。
	p := &argus.AuthPermissions{
		ReadRepository:   isRead,
		RequestReview:    isCI,
		DecideReview:     isAdmin,
		ApplyFix:         isAdmin,
		MergePr:          isAdmin,
		ReadAudit:        isAdmin,
		ManageMembership: isAdmin && role == storage.RoleAdmin,
		ManageInstance:   isAdmin && role == storage.RoleAdmin,
	}
	// 非 admin 角色即使 scope=admin（maintainer）也不给 manage_*。
	if role != "" && role != storage.RoleAdmin {
		p.ManageMembership = false
		p.ManageInstance = false
		// maintainer：业务裁定面（decide/merge/fix）保留。
		if role == storage.RoleMaintainer {
			p.DecideReview, p.ApplyFix, p.MergePr = true, true, true
			p.ReadAudit = false // 审计读留给 admin/auditor（后续扩）
		}
	}
	return p
}

func authInvite(s *Service, ctx context.Context, req *argus.AuthInviteRequest) (*argus.AuthInviteResponse, int) {
	// 只有 admin 角色（或无会话的静态 admin token）可邀请。
	if username, role, ok := SessionFromContext(ctx); ok {
		if role != storage.RoleAdmin {
			return nil, http.StatusForbidden
		}
		_ = username
	} else if ScopeFromContext(ctx) != "admin" {
		return nil, http.StatusForbidden
	}
	role := strings.TrimSpace(req.GetRole())
	if role == "" || !storage.ValidRole(role) || role == storage.RoleAdmin {
		return nil, http.StatusBadRequest // 邀请永不授予 admin
	}
	ttl := inviteTTL
	if req.TTLHours != nil && *req.TTLHours > 0 {
		ttl = time.Duration(*req.TTLHours) * time.Hour
	}
	createdBy, _, _ := SessionFromContext(ctx)
	if createdBy == "" {
		createdBy = "static-admin"
	}
	token, inv, err := s.Store.Auths().CreateInvite(ctx, role, optionalAuthString(req.Username), createdBy, ttl)
	if err != nil {
		s.log().Warn("argus.auth.invite_failed", "error", err.Error())
		return nil, http.StatusBadRequest
	}
	if s.Audit != nil {
		s.Audit.Log("auth.invite_created", "invite_id", inv.ID, "role", role, "by", createdBy)
	}
	return &argus.AuthInviteResponse{
		InviteID: inv.ID, Token: token, Role: role, ExpiresAt: inv.ExpiresAt.Unix(),
	}, http.StatusOK
}

func authAcceptInvite(s *Service, ctx context.Context, req *argus.AuthAcceptInviteRequest) (*argus.AuthAcceptInviteResponse, int) {
	if req.GetToken() == "" || req.GetUsername() == "" || req.GetPassword() == "" {
		return nil, http.StatusBadRequest
	}
	u, err := s.Store.Auths().AcceptInvite(ctx, req.GetToken(), req.GetUsername(), req.GetPassword())
	if err != nil {
		return nil, http.StatusUnauthorized
	}
	if s.Audit != nil {
		s.Audit.Log("auth.invite_accepted", "username", u.Username, "role", u.Role)
	}
	return &argus.AuthAcceptInviteResponse{
		Username: u.Username, Role: u.Role, Created: true,
	}, http.StatusOK
}

func optionalAuthString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
