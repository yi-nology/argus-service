// service_auth.go —— API 鉴权面：ARGUS_API_TOKENS 多 token 三档 scope + Bearer 中间件（fail-closed）。

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

// BearerMiddleware 🔒 路由的 Bearer 静态 token 校验（§5；webhook 不挂，靠平台签名）。
// scopeRank scope→覆盖等级闭集（read < ci < admin）。与 config.apiTokenScopeRank
// 同值——service 侧自持避免依赖方向变化，两表一致性由 rbac_test 钉住。
var scopeRank = map[string]int{"read": 1, "ci": 2, "admin": 3}

// authToken 中间件视图里的一条 token（旧单 token 等价 admin 条目）。
type authToken struct {
	token   string
	scope   string
	repos   string    // 可选仓库约束（v3.135.0）；空 = 不限
	expires time.Time // 过期时间（v3.136.0）；零值 = 永不过期
}

// authTokens 生效 token 表：ARGUS_APITOKEN（admin）+ ARGUS_API_TOKENS 条目。
func (s *Service) authTokens() []authToken { return authTokens(s) }

// requiredScope 方法+路径 → 所需权限档：/controlplane/*（控制面人工介入，读写
// 一律全局 admin）、POST /findings/*（误报抑制写库）与 PUT|DELETE
// /repo-settings*（按仓配置是信任面写入）= admin；其余 POST/DELETE（建任务、
// MCP 会话）= ci；PUT 兜底 admin——写语义缺省最严档，新增写路由漏配 scope 时
// fail-closed 而非裸放 read；读面 = read。
func requiredScope(method, path string) string {
	switch {
	case strings.HasPrefix(path, "/controlplane/"):
		// 控制面没有「只读」档：列 unknown/租约本身就是处置现场信息，GET 也要 admin。
		return "admin"
	case method == http.MethodPost && strings.HasPrefix(path, "/findings/"):
		return "admin"
	case strings.HasPrefix(path, "/repo-settings") &&
		(method == http.MethodPut || method == http.MethodDelete):
		return "admin"
	case method == http.MethodPost || method == http.MethodDelete:
		return "ci"
	case method == http.MethodPut:
		return "admin"
	default:
		return "read"
	}
}

// scopeKey 请求鉴权 scope 的 ctx 键（BearerMiddleware 写入，工具级二次判定读取）。
type scopeKey struct{}

// ContextWithScope 把鉴权授予的 scope 写入 ctx（v3.128.0 scope 透传）。
func ContextWithScope(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, scopeKey{}, scope)
}

// ScopeFromContext 取请求的鉴权 scope；未鉴权路径返回空串（fail-closed 判据）。
func ScopeFromContext(ctx context.Context) string {
	s, _ := ctx.Value(scopeKey{}).(string)
	return s
}

// tokenFPKey 请求 token 指纹的 ctx 键（v3.137.0 取证面）。
type tokenFPKey struct{}

// ContextWithTokenFP 写入请求 token 指纹（sha256 前 8 字节 hex，非原 token）。
func ContextWithTokenFP(ctx context.Context, fp string) context.Context {
	return context.WithValue(ctx, tokenFPKey{}, fp)
}

// TokenFPFromContext 取请求 token 指纹（内部路径无 token = 空串）。
func TokenFPFromContext(ctx context.Context) string {
	s, _ := ctx.Value(tokenFPKey{}).(string)
	return s
}

// fail-closed（v3.0.1 修订——原实现在 APIToken 未配置时直接放行，等于鉴权裸奔）：
// token 表全空（ARGUS_APITOKEN 与 ARGUS_API_TOKENS 均未配置）时 🔒 路由一律拒绝
// 服务（webhook/健康检查不受影响）。v3.87.0 起带 scope 分级：命中 token 后按
// 方法+路径所需档位校验（admin ⊇ ci ⊇ read），不足 403。
func (s *Service) BearerMiddleware() app.HandlerFunc { return bearerMiddleware(s) }

// tokenRateOK 每 token 速率闸（v3.135.0；v3.147.0 换 oekit ratelimit.KeyedLimiter）：
// 每 token 独立令牌桶（rpm/60 速率、burst=max(10,rpm/6) 与此前 x/time/rate 参数
// 等价），闲置 1h 的 key 由库清理协程驱逐（token 集合有界，驱逐仅重置闲置桶）。
// APIRateRPM<=0 = 关闭；sync.Once 懒建，不开限速的实例不起清理协程。
func (s *Service) tokenRateOK(rawToken string) bool { return tokenRateOK(s, rawToken) }

// tokenHash 限速键指纹（不把原始 token 放 map 键）。
func tokenHash(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:8])
}

// ---------- TaskAPI ----------
