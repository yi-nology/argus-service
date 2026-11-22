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

// canDecide 业务裁定面（审批票 / 合并 / 受控推送）。
// 平台管理与业务裁定**物理分离**（MergePilot 同款）：
//   - 会话 role=maintainer → 可裁定，不可管理
//   - 会话 role=admin     → 可管理，**不可裁定**（platform_admin 刻意无 merge）
//   - 静态 token（无会话）admin 档 → 部署超管，两面皆可（冷启动/运维）
func canDecide(ctx context.Context) bool {
	if ScopeFromContext(ctx) != "admin" {
		return false
	}
	_, role, ok := SessionFromContext(ctx)
	if !ok {
		return true // 静态 token = 部署超管
	}
	return role == storage.RoleMaintainer
}

// canManage 平台管理面（邀请成员 / 实例配置）。
// 会话仅 role=admin；静态 token admin 档 = 部署超管。
func canManage(ctx context.Context) bool {
	if ScopeFromContext(ctx) != "admin" {
		return false
	}
	_, role, ok := SessionFromContext(ctx)
	if !ok {
		return true
	}
	return role == storage.RoleAdmin
}

// permissionsForScope my_permissions 投影（MergePilot 同名能力对齐）。
// 会话角色非递增：maintainer 与 admin 分属裁定面/管理面，互不包含。
func permissionsForScope(scope, role string) *argus.AuthPermissions {
	isAdmin := scope == "admin"
	isCI := isAdmin || scope == "ci"
	isRead := isCI || scope == "read"
	p := &argus.AuthPermissions{
		ReadRepository: isRead,
		RequestReview:  isCI,
	}
	switch {
	case role == storage.RoleMaintainer:
		// 业务裁定面：审批/合并/推送；无管理、无审计读。
		p.DecideReview, p.ApplyFix, p.MergePr = true, true, true
	case role == storage.RoleAdmin:
		// 平台管理面：成员/实例；刻意无裁定（防平台管理员越权合码）。
		p.ManageMembership, p.ManageInstance = true, true
		p.ReadAudit = true
	case role == "":
		// 静态 token（部署超管）：admin 档全开，低档按 scope。
		p.DecideReview, p.ApplyFix, p.MergePr = isAdmin, isAdmin, isAdmin
		p.ReadAudit = isAdmin
		p.ManageMembership, p.ManageInstance = isAdmin, isAdmin
	default:
		// viewer / auditor / operator / 未知：只读或 ci，无裁定无管理。
	}
	return p
}

func authInvite(s *Service, ctx context.Context, req *argus.AuthInviteRequest) (*argus.AuthInviteResponse, int) {
	// 平台管理面：会话 admin 角色或部署超管；maintainer 不可邀请（物理分离）。
	if !canManage(ctx) {
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
