// service_impl_auth.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/oekit/concurrency/ratelimit"
	mw "github.com/oekit/middleware"
	"golang.org/x/time/rate"

	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// authTokens 的包级实现（转发壳见原文件）。
func authTokens(s *Service) []authToken {
	var out []authToken
	if s.Cfg.APIToken != "" {
		out = append(out, authToken{token: s.Cfg.APIToken, scope: "admin"})
	}
	for _, t := range s.Cfg.APITokens {
		out = append(out, authToken{token: t.Token, scope: t.Scope, repos: t.Repos, expires: t.Expires, tenant: t.Tenant})
	}
	return out
}

// bearerMiddleware 的包级实现（转发壳见原文件）。
// vT5：在配置静态 token 之外支持**会话 token**（/auth/login 签发，存 DB 哈希）。
// fail-closed 纪律不变：配置 token 全空且会话也解析失败 → 503/401；
// 仅当配置 token 全空时不再直接 503——先试会话（多用户最小集），会话也无才 503。
func bearerMiddleware(s *Service) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokens := s.authTokens()
		auth := string(c.GetHeader("Authorization"))
		granted, grantedRepos, grantedFP, grantedTenant := "", "", "", ""
		expired := false
		sessionUser := ""
		sessionRole := ""

		// 1) 配置静态 token（行为与存量一致：常数时间比较 + 过期即拒）。
		for _, t := range tokens {
			if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+t.token)) == 1 {
				if !t.expires.IsZero() && time.Now().After(t.expires) {
					expired = true
					break
				}
				granted, grantedRepos = t.scope, t.repos
				grantedFP = tokenHash(t.token)[:8]
				grantedTenant = t.tenant
				break
			}
		}
		// 2) 会话 token（DB；配置 token 未命中时）。
		if granted == "" && !expired && s.Store != nil && len(auth) > 8 {
			raw := strings.TrimPrefix(auth, "Bearer ")
			if raw != auth && raw != "" {
				sess, user, err := s.Store.Auths().GetSessionByToken(ctx, raw)
				if err == nil && sess != nil && user != nil {
					granted = storage.RoleToScope(sess.Role)
					grantedFP = tokenHash(raw)[:8]
					sessionUser = user.Username
					sessionRole = sess.Role
				}
			}
		}
		if expired {
			s.log().Warn("argus.auth.token_expired", "path", string(c.URI().Path()))
			mw.ErrorResp(c, http.StatusUnauthorized, "token 已过期（轮换：清单里挂新 token 并行过渡后移除旧条目）")
			return
		}
		if granted == "" {
			// fail-closed：配置与会话均未授予。
			if len(tokens) == 0 {
				s.log().Warn("argus.auth.rejected_no_token",
					"path", string(c.URI().Path()),
					"hint", "ARGUS_APITOKEN/ARGUS_API_TOKENS 与会话均未授予，🔒 路由拒绝服务")
				mw.ErrorResp(c, http.StatusServiceUnavailable,
					"鉴权未配置：无静态 token 且会话无效，🔒 路由拒绝服务（webhook 与健康检查不受影响）")
				return
			}
			mw.ErrorResp(c, http.StatusUnauthorized, "缺少或错误的 Authorization: Bearer token")
			return
		}
		need := requiredScope(string(c.Method()), string(c.Path()))
		if scopeRank[granted] < scopeRank[need] {
			s.log().Warn("argus.auth.scope_denied", "path", string(c.URI().Path()),
				"granted", granted, "required", need, "user", sessionUser, "role", sessionRole)
			mw.ErrorResp(c, http.StatusForbidden,
				fmt.Sprintf("token 权限档不足（%s < %s）", granted, need))
			return
		}
		if !s.tokenRateOK(ctx, auth) {
			mw.ErrorResp(c, http.StatusTooManyRequests,
				fmt.Sprintf("调用频率超限（每 token %d 次/分钟，ARGUS_API_RATE_RPM）", s.Cfg.APIRateRPM))
			return
		}
		ctx = tenant.NewContext(ctx, grantedTenant)
		ctx = ContextWithTokenFP(ContextWithTokenRepos(ContextWithScope(ctx, granted), grantedRepos), grantedFP)
		if sessionUser != "" {
			ctx = ContextWithSession(ctx, sessionUser, sessionRole)
		}
		c.Next(ctx)
	}
}

// sessionKey 会话身份 ctx 键（/auth/me 与审计用）。
type sessionKey struct{}

// ContextWithSession 写入会话身份（用户名+角色）。
func ContextWithSession(ctx context.Context, username, role string) context.Context {
	return context.WithValue(ctx, sessionKey{}, sessionIdentity{Username: username, Role: role})
}

type sessionIdentity struct{ Username, Role string }

// SessionFromContext 取会话身份；静态 token 路径返回 ok=false。
func SessionFromContext(ctx context.Context) (username, role string, ok bool) {
	s, _ := ctx.Value(sessionKey{}).(sessionIdentity)
	return s.Username, s.Role, s.Username != ""
}

// tokenRateOK 的包级实现（转发壳见原文件）。
func tokenRateOK(s *Service, ctx context.Context, rawToken string) bool {
	if s.Cfg == nil || s.Cfg.APIRateRPM <= 0 {
		return true
	}
	rpm := s.Cfg.APIRateRPM
	s.rateOnce.Do(func() {
		s.rateLimiter = ratelimit.NewKeyedLimiter(func() ratelimit.Limiter {
			return ratelimit.NewTokenBucket(rate.Limit(float64(rpm)/60.0), max(10, rpm/6))
		}, time.Hour)
	})
	// #104：限速键并入租户段——同 token 在不同租户维度独立桶（token 已带
	// 租户声明时 tokenHash 天然分桶；ctx 租户作第二维度兜内部路径）。
	return s.rateLimiter.Allow(tenant.Key(tenant.FromContext(ctx), tokenHash(rawToken)))
}

// authorize 的包级实现（转发壳见原文件）。
func authorize(s *Service, ctx context.Context, platform, owner, repo string) bool {
	return s.repoAllowedFor(ctx, platform, owner, repo)
}

// authorizeTask 的包级实现（转发壳见原文件）。
func authorizeTask(s *Service, ctx context.Context, platform, owner, repo string) bool {
	return s.taskVisible(ctx, platform, owner, repo)
}

// authorizeEvent 的包级实现（转发壳见原文件）。事件租户维度（#105）：
// local/空坐标事件没有仓坐标可判，按事件 Tenant 与 ctx 租户比对——同租户
// 或双方 default 放行；跨租户拒绝（此前 local 直通=任意 read token 可见
// 全部 local 任务元数据的串扰面）。
func authorizeEvent(s *Service, ctx context.Context, platform, owner, repo, evTenant string) bool {
	caller := tenant.FromContext(ctx)
	et := tenant.Normalize(evTenant)
	if platform == "" && owner == "" && repo == "" {
		return et == caller // 无仓语义事件：纯租户维度
	}
	if platform == "local" {
		return et == caller // local 空坐标：纯租户维度（替代旧直通）
	}
	if et != caller && et != tenant.Default {
		return false // 事件租户与调用租户不匹配（事件不带租户字段的旧事件
		// 归 default——与 default 调用方互见，跨租户不可见）
	}
	tp := TokenReposFromContext(ctx)
	if tp == "" {
		return true
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}

// authorizeRowFilter 的包级实现（转发壳见原文件）。
func authorizeRowFilter(s *Service, ctx context.Context) func(platform, owner, repo string) bool {
	tp := TokenReposFromContext(ctx)
	if tp == "" {
		return nil
	}
	return func(platform, owner, repo string) bool {
		return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
	}
}
