// runner_wc_tenant.go —— 工作副本池的租户解析（#81）。

package runner

import (
	"context"

	"github.com/oagentkit/tools/workcopy"

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
