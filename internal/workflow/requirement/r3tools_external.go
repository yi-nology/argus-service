// r3tools_external.go —— 外部 MCP 工具进表：排序、风险分级与调用上限。

package requirement

import (
	"context"
	"sort"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/toolgate/toolprior"
)

// r3AddExternalTools 外部 MCP 工具进表：按名排序（缓存前缀纪律）→ 风险分级
// 注解 → External 档 + 调用上限硬止损（v3.41.0 起上限可配，缺省 5）。
func (w *Workflow) r3AddExternalTools(ctx context.Context, table *toolprior.Table) {
	r3AddExternalTools(w, ctx, table)
}

// asInvokable BaseTool → InvokableTool 断言（不可调用工具跳过外部限制包装）。
func asInvokable(t tool.BaseTool) tool.InvokableTool {
	if it, ok := t.(tool.InvokableTool); ok {
		return it
	}
	return nil
}

// sortToolsByName 工具表按名排序（原位）。Info 失败的工具保持相对位置（稳定排序）。
func sortToolsByName(ctx context.Context, tools []tool.BaseTool) {
	sort.SliceStable(tools, func(i, j int) bool {
		ni, erri := tools[i].Info(ctx)
		nj, errj := tools[j].Info(ctx)
		if erri != nil || errj != nil {
			return false
		}
		return ni.Name < nj.Name
	})
}
