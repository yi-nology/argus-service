// r3tools_external.go —— 外部 MCP 工具进表：排序、风险分级与调用上限。

package requirement

import (
	"context"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/toolgate/toolprior"
)

// r3AddExternalTools 外部 MCP 工具进表：按名排序（缓存前缀纪律）→ 风险分级
// 注解 → External 档 + 调用上限硬止损（v3.41.0 起上限可配，缺省 5）。
func (w *Workflow) r3AddExternalTools(ctx context.Context, table *toolprior.Table) {
	if w.Tools == nil {
		return
	}
	extra, err := w.Tools.Tools(ctx, w.ToolSpecs)
	if err != nil {
		w.Log.Warn("argus.r3.toolpool_error", "error", err.Error())
		return
	}
	// 缓存前缀纪律（v3.38.0）：MCP 目录来自 map 迭代、顺序不定——按名排序
	// 后再注册，保证工具表与策略提示逐字节稳定（provider context cache 前提）
	sortToolsByName(ctx, extra)
	// 风险分级（v3.39.0）：目录注解驱动——破坏性工具加显式警示并升成本档
	hints := w.Tools.Hints(w.ToolSpecs)
	for _, t := range extra {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		note := ""
		cost := "high"
		if h, ok := hints[info.Name]; ok && h.Destructive {
			note = "；⚠️ 该工具具有破坏性副作用，调用前确认必要性与最小影响范围"
			cost = "heavy"
		}
		// 外部工具：每次调用有网络/进程开销 → External 档 + 调用上限硬止损
		//（v3.41.0 起上限可配 ARGUS_EXTERNAL_TOOL_CALL_LIMIT，缺省 5）
		limit := w.ExternalToolCallLimit
		if limit <= 0 {
			limit = 5
		}
		table.Add(toolprior.Entry{
			Tool:     toolprior.LimitCalls(asInvokable(t), limit),
			Priority: toolprior.PriorityExternal,
			When:     fmt.Sprintf("外部系统工具（%s）：本地证据穷尽后再用%s", info.Name, note),
			Cost:     cost,
		})
	}
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
