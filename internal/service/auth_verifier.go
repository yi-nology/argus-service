// auth_verifier.go —— oekit/middleware v0.23 TokenVerifier 收编（升级红利 #293）。
//
// 静态 token（ARGUS_APITOKEN/_TOKENS）与会话 token（users/sessions 表）统一
// 经 TokenVerifier 校验；Subject.Claims 透传 scope/repos/tenant/fp/username/role，
// 消费侧走 mw.SubjectFrom 或既有 ContextWith* 门面。
//
// 错误分类对齐上游契约：
//   - ErrInvalidToken → 401（凭证无效）
//   - ErrForbidden    → 403（凭证有效但无权）
//   - 其它 error      → 500（后端故障，不伪装 401 诱发客户端轮换凭据）
package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	mw "github.com/oekit/middleware"

	"github.com/yi-nology/argus/internal/storage"
)

// tokenVerifier 实现 mw.TokenVerifier：静态 token + 会话 token 双源。
type tokenVerifier struct{ s *Service }

// verifyResult 校验通过的内部载荷（映射进 Subject.Claims）。
type verifyResult struct {
	scope    string
	repos    string
	tenant   string
	fp       string
	username string // 会话用户（静态 token 空）
	role     string
}

// Verify 实现 mw.TokenVerifier。
func (v tokenVerifier) Verify(ctx context.Context, c *app.RequestContext, token string) (mw.Subject, error) {
	res, err := v.s.verifyBearerToken(ctx, token)
	if err != nil {
		return mw.Subject{}, err
	}
	sub := mw.Subject{
		UserID: res.username,
		Roles:  []string{res.role},
		Claims: map[string]any{
			"scope":    res.scope,
			"repos":    res.repos,
			"tenant":   res.tenant,
			"token_fp": res.fp,
			"username": res.username,
			"role":     res.role,
		},
	}
	if res.role == "" {
		sub.Roles = nil
	}
	return sub, nil
}

// verifyBearerToken 双源校验（原 bearerMiddleware 内联逻辑收编）。
// token 为**不带 "Bearer " 前缀**的明文。
func (s *Service) verifyBearerToken(ctx context.Context, token string) (*verifyResult, error) {
	tokens := s.authTokens()
	if len(tokens) == 0 && s.Store == nil {
		return nil, mw.ErrInvalidToken
	}
	// 1) 静态 token（常数时间比较）。
	for _, t := range tokens {
		if subtle.ConstantTimeCompare([]byte(token), []byte(t.token)) == 1 {
			if !t.expires.IsZero() && time.Now().After(t.expires) {
				return nil, fmt.Errorf("token 已过期: %w", mw.ErrInvalidToken)
			}
			return &verifyResult{
				scope: t.scope, repos: t.repos, tenant: t.tenant,
				fp: tokenHash(t.token)[:8],
			}, nil
		}
	}
	// 2) 会话 token（DB 哈希查找）。
	if s.Store != nil && token != "" {
		sess, user, err := s.Store.Auths().GetSessionByToken(ctx, token)
		if err == nil && sess != nil && user != nil {
			return &verifyResult{
				scope: storage.RoleToScope(sess.Role), tenant: tenantOfSession(sess, user),
				fp: tokenHash(token)[:8], username: user.Username, role: sess.Role,
			}, nil
		}
	}
	return nil, mw.ErrInvalidToken
}

func tenantOfSession(sess *storage.Session, _ *storage.User) string {
	if sess == nil {
		return ""
	}
	return sess.TenantID
}

// scopeAuthorizer TokenVerifier 通过后的授权钩子：requiredScope 档位判定。
// 拒绝返回 (false, nil) → 中间件 403。
type scopeAuthorizer struct{}

func (scopeAuthorizer) Authorize(_ context.Context, c *app.RequestContext, sub mw.Subject) (bool, error) {
	need := requiredScope(string(c.Method()), string(c.Path()))
	have := ""
	if sub.Claims != nil {
		if sc, ok := sub.Claims["scope"].(string); ok {
			have = sc
		}
	}
	return scopeRank[have] >= scopeRank[need], nil
}

// claimsFrom 把 Subject.Claims 拍平成我们的 ctx 五件套。
func claimsFrom(sub mw.Subject) (scope, repos, tenantID, fp, username, role string) {
	if sub.Claims == nil {
		return
	}
	str := func(k string) string {
		if v, ok := sub.Claims[k].(string); ok {
			return v
		}
		return ""
	}
	return str("scope"), str("repos"), str("tenant"), str("token_fp"), str("username"), str("role")
}

// stripBearer 去掉 "Bearer " 前缀（大小写不敏感）。
func stripBearer(auth string) string {
	const p = "Bearer "
	if len(auth) > len(p) && strings.EqualFold(auth[:len(p)], p) {
		return strings.TrimSpace(auth[len(p):])
	}
	return strings.TrimSpace(auth)
}

// sessionKey 会话身份 ctx 键（/auth/me 与审计用）。
type sessionKey struct{}

type sessionIdentity struct{ Username, Role string }

// ContextWithSession 写入会话身份（用户名+角色）。
func ContextWithSession(ctx context.Context, username, role string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionIdentity{Username: username, Role: role})
}

// SessionFromContext 取会话身份；静态 token 路径返回 ok=false。
func SessionFromContext(ctx context.Context) (username, role string, ok bool) {
	s, _ := ctx.Value(sessionKey{}).(sessionIdentity)
	return s.Username, s.Role, s.Username != ""
}

// 编译期契约：tokenVerifier / scopeAuthorizer 必须满足 oekit/middleware 接口
// （升级红利 #293 的接入面——接口漂移在这里红，不是生产 401 时才现形）。
var (
	_ mw.TokenVerifier = tokenVerifier{}
	_ mw.Authorizer    = scopeAuthorizer{}
)
