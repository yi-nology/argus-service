// service_impl_auth.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/oekit/concurrency/ratelimit"
	mw "github.com/oekit/middleware"
	"golang.org/x/time/rate"

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
// v0.23 吸收（middleware#293 TokenVerifier 语义）：token 校验走
// verifyBearerToken（auth_verifier.go，双源 + ErrInvalidToken/ErrForbidden
// 分类），授权走 requiredScope 档位；ctx 注入五件套保持既有契约。
// 不嵌套 mw.BearerTokenAuth——它内部会 c.Next 跑完整 handler 链，
// 与本中间件的「鉴权→限速→注入→Next」顺序冲突。
func bearerMiddleware(s *Service) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokens := s.authTokens()
		raw := stripBearer(string(c.GetHeader("Authorization")))
		// fail-closed：配置 token 全空且未带任何凭证时 503（存量语义）。
		if len(tokens) == 0 && raw == "" {
			s.log().Warn("argus.auth.rejected_no_token",
				"path", string(c.URI().Path()),
				"hint", "ARGUS_APITOKEN/ARGUS_API_TOKENS 与会话均未授予，🔒 路由拒绝服务")
			mw.ErrorResp(c, http.StatusServiceUnavailable,
				"鉴权未配置：无静态 token 且会话无效，🔒 路由拒绝服务（webhook 与健康检查不受影响）")
			return
		}
		// 限速（被盗 token 刷接口先挡；429 不落审计）。
		if raw != "" && !s.tokenRateOK(ctx, raw) {
			mw.ErrorResp(c, http.StatusTooManyRequests,
				fmt.Sprintf("调用频率超限（每 token %d 次/分钟，ARGUS_API_RATE_RPM）", s.Cfg.APIRateRPM))
			return
		}
		// TokenVerifier 语义校验（静态 + 会话双源）。
		res, err := s.verifyBearerToken(ctx, raw)
		if err != nil {
			if errors.Is(err, mw.ErrInvalidToken) {
				s.log().Warn("argus.auth.invalid_token", "path", string(c.URI().Path()))
				mw.ErrorResp(c, http.StatusUnauthorized, "缺少或错误的 Authorization: Bearer token")
				return
			}
			// 后端故障不伪装 401（middleware#293 分类）。
			s.log().Error("argus.auth.verify_error", "path", string(c.URI().Path()), "error", err.Error())
			mw.ErrorResp(c, http.StatusInternalServerError, "authentication error")
			return
		}
		// 授权：requiredScope 档位（scopeAuthorizer 同判据）。
		need := requiredScope(string(c.Method()), string(c.Path()))
		if scopeRank[res.scope] < scopeRank[need] {
			s.log().Warn("argus.auth.scope_denied", "path", string(c.URI().Path()),
				"granted", res.scope, "required", need, "user", res.username, "role", res.role)
			mw.ErrorResp(c, http.StatusForbidden,
				fmt.Sprintf("token 权限档不足（%s < %s）", res.scope, need))
			return
		}
		// ctx 注入五件套 + 会话身份（消费侧零改动）。
		ctx = tenant.NewContext(ctx, res.tenant)
		ctx = ContextWithTokenFP(ContextWithTokenRepos(ContextWithScope(ctx, res.scope), res.repos), res.fp)
		if res.username != "" {
			ctx = ContextWithSession(ctx, res.username, res.role)
		}
		c.Next(ctx)
	}
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
