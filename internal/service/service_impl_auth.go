// service_impl_auth.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"crypto/subtle"
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
func bearerMiddleware(s *Service) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokens := s.authTokens()
		if len(tokens) == 0 {
			s.log().Warn("argus.auth.rejected_no_token",
				"path", string(c.URI().Path()), "hint", "ARGUS_APITOKEN/ARGUS_API_TOKENS 均未配置，🔒 路由拒绝服务")
			mw.ErrorResp(c, http.StatusServiceUnavailable,
				"鉴权未配置：服务端未设置 ARGUS_APITOKEN/ARGUS_API_TOKENS，🔒 路由拒绝服务（webhook 与健康检查不受影响）")
			return
		}
		auth := string(c.GetHeader("Authorization"))
		granted, grantedRepos, grantedFP, grantedTenant := "", "", "", ""
		expired := false
		for _, t := range tokens {
			// 常数时间比较：与 SDK webhook 签名校验的纪律对齐（内部静态 token，纵深防御）
			if subtle.ConstantTimeCompare([]byte(auth), []byte("Bearer "+t.token)) == 1 {
				if !t.expires.IsZero() && time.Now().After(t.expires) {
					// 过期即拒（v3.136.0 轮换语义）：不给"匹配但过期"留任何灰度窗口，
					// 换发新 token 由部署方在清单里并行挂两条完成。
					expired = true
					break
				}
				granted, grantedRepos = t.scope, t.repos
				grantedFP = tokenHash(t.token)[:8]
				grantedTenant = t.tenant
				break
			}
		}
		if expired {
			s.log().Warn("argus.auth.token_expired", "path", string(c.URI().Path()))
			mw.ErrorResp(c, http.StatusUnauthorized, "token 已过期（轮换：清单里挂新 token 并行过渡后移除旧条目）")
			return
		}
		if granted == "" {
			mw.ErrorResp(c, http.StatusUnauthorized, "缺少或错误的 Authorization: Bearer token")
			return
		}
		need := requiredScope(string(c.Method()), string(c.Path()))
		if scopeRank[granted] < scopeRank[need] {
			s.log().Warn("argus.auth.scope_denied", "path", string(c.URI().Path()),
				"granted", granted, "required", need)
			mw.ErrorResp(c, http.StatusForbidden,
				fmt.Sprintf("token 权限档不足（%s < %s）", granted, need))
			return
		}
		// 每 token 速率限制（v3.135.0，ARGUS_API_RATE_RPM；0=关）：被盗 token
		// 刷 LLM 成本/刷审查的闸。429 不落审计（高频噪音），fail-closed 语义不变。
		if !s.tokenRateOK(auth) {
			mw.ErrorResp(c, http.StatusTooManyRequests,
				fmt.Sprintf("调用频率超限（每 token %d 次/分钟，ARGUS_API_RATE_RPM）", s.Cfg.APIRateRPM))
			return
		}
		// scope+repos 透传（v3.128.0/v3.135.0）：/mcp 路由本身只需 ci 档，但部分
		// 工具是 admin 写操作、且 token 可带仓库约束——把授予档位与仓库范围写进
		// ctx，工具级二次判定/仓库门在此收口。
		// 租户透传（#103）：认证租户的存储谓词/审计/计费归属——无声明归 default
		// （tenant.NewContext 内 Normalize，存量部署零变化）。
		ctx = tenant.NewContext(ctx, grantedTenant)
		c.Next(ContextWithTokenFP(ContextWithTokenRepos(ContextWithScope(ctx, granted), grantedRepos), grantedFP))
	}
}

// tokenRateOK 的包级实现（转发壳见原文件）。
func tokenRateOK(s *Service, rawToken string) bool {
	if s.Cfg == nil || s.Cfg.APIRateRPM <= 0 {
		return true
	}
	rpm := s.Cfg.APIRateRPM
	s.rateOnce.Do(func() {
		s.rateLimiter = ratelimit.NewKeyedLimiter(func() ratelimit.Limiter {
			return ratelimit.NewTokenBucket(rate.Limit(float64(rpm)/60.0), max(10, rpm/6))
		}, time.Hour)
	})
	return s.rateLimiter.Allow(tokenHash(rawToken))
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
