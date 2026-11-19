// runner_wc_tenant.go —— 工作副本池的租户解析（#81）。

package runner

import (
	"context"

	"github.com/oagentkit/tools/workcopy"

	"github.com/yi-nology/argus/internal/gitdeepen"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/tenant"
)

// wcFor 任务 ctx 的工作副本池：ctx 租户配了独立池 → 租户池（克隆目录与
// 该租户凭证指纹绑定）；否则默认池。Ensure/Release 同一 ctx 调用成对出现，
// 解析天然一致。acpx 插件与 QA 检索面仍直用 r.WC（默认池）——残余面在案。
func (r *Runner) wcFor(ctx context.Context) *workcopy.Pool {
	if tid := tenant.FromContext(ctx); tid != tenant.Default && len(r.TenantWCPools) > 0 {
		if p, ok := r.TenantWCPools[tid]; ok && p != nil {
			return p
		}
	}
	return r.WC
}

// deepenAuth deepen fetch 的凭证与期望 clone 源（tools#160/#184）：与 workcopy
// 克隆同源（Pool.CredentialOf，含 GitHub App token 分支）——clone/PR fetch/
// refresh 三路自 tools#124 起走无凭证 URL + 进程级凭证，deepen 是唯一漏注的
// fetch；私有平台浅克隆加深必 "could not read Username"，函数级 diff / OCR
// delegate preview 每次静默降级。凭证解析不在 impactview/ocrdeleg 侧复刻。
func (r *Runner) deepenAuth(ctx context.Context, pr *plugin.PRContext) gitdeepen.Auth {
	pool := r.wcFor(ctx)
	if pool == nil || pool.CredentialOf == nil || pr == nil {
		return gitdeepen.Auth{}
	}
	baseURL, token, ok := pool.CredentialOf(pr.Platform)
	if !ok {
		return gitdeepen.Auth{}
	}
	return gitdeepen.Auth{Token: token, BaseURL: baseURL, Owner: pr.Owner, Repo: pr.Repo}
}
