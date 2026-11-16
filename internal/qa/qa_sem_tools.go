package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/yi-nology/argus/internal/semsearch"
)

// search_sem/get_trace/get_symbol_context 的入参钳制与渲染预算（MCP 工具描述
// 「缺省 5、上界 10」「≤4 跳」与本值同源手写，改此须同步）。
const (
	semSearchDefaultTopK = 5
	semSearchMaxTopK     = 10
	semHitTextCap        = 600 // 单条命中摘要渲染截断（rune）
	semSearchNeighborCap = 3   // search_sem 命中邻接单侧封顶（调用方 3 + 被调 3，合计 ≤6）
	symContextCap        = 8   // get_symbol_context 调用方/被调单侧封顶（与底座 graphCallersCap 同源）
	traceHops            = 4   // get_trace 跳数（与 MCP 工具描述「≤4 跳」同源手写，改此须同步）
)

// 图邻接关系词（semsearch.SymRef.Rel；词表源在 semsearch/graph.go，包内未导出）。
const (
	semRelCaller   = "caller"    // 调用方（邻接面）
	semRelCallee   = "callee"    // 被调（邻接面）
	semRelCalls    = "calls"     // 本步调用下一步（调用链面）
	semRelCalledBy = "called_by" // 本步被下一步调用（调用链面）
)

// renderSemNeighbors 图邻接单行渲染（v5.9.0，search_sem 命中附邻接与
// get_symbol_context 共用）：按 Rel 分「调用方/被调」两组各取前 cap 个（cap 为
// 单组上限），组内顿号连接、组间分号连接——`调用方 A(a.go:12)、B(b.go:34)；被调
// C(c.go:56)`；无定义位置的符号只显符号名（loc 缺失与底座 Path 输出同语义）；
// 空组省略，两组皆空返回空串（调用方据此整行省略）。
func renderSemNeighbors(ns []semsearch.SymRef, cap int) string {
	segment := func(label string, refs []semsearch.SymRef) string {
		if len(refs) == 0 {
			return ""
		}
		parts := make([]string, 0, len(refs))
		for _, r := range refs {
			if r.Path == "" {
				parts = append(parts, r.Symbol)
				continue
			}
			parts = append(parts, fmt.Sprintf("%s(%s:%d)", r.Symbol, r.Path, r.Line))
		}
		return label + " " + strings.Join(parts, "、")
	}
	var callers, callees []semsearch.SymRef
	for _, r := range ns {
		switch r.Rel {
		case semRelCaller:
			if len(callers) < cap {
				callers = append(callers, r)
			}
		case semRelCallee:
			if len(callees) < cap {
				callees = append(callees, r)
			}
		}
	}
	a, b := segment("调用方", callers), segment("被调", callees)
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "；" + b
}

// SearchSem 工作副本语义检索（v3.126.0，MCP search_sem 数据面）：按意图/自然
// 语言找代码——结构化摘要 embedding + 进程内余弦，补 search_code（词法正则）
// 答不了的「哪里处理认证/哪里刷新 token」类问题。启用双门：SemSearch 底座
// （ARGUS_SEMSEARCH + ARGUS_EMBEDDING_*）+ 工作副本池；未启用返回 error
// （环境面故障，与 SearchCode 同型），无命中返回提示文本。
func (e *Engine) SearchSem(ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error) {
	return searchSem(e, ctx, platform, owner, repo, number, query, topK)
}

// edgeQualityNote 图面边界措辞随精确边模式切换（v5.10.0）：ARGUS_GRAPH_PRECISE
// 部署开启且该目录索引成功应用精确边时为 go/types 类型解析级（接口分派仍不可
// 见），否则维持词法级。get_trace/get_symbol_context 围栏与提示文本共用。
func edgeQualityNote(precise bool) string {
	if precise {
		return "类型解析级边（go/types；接口分派不可见）"
	}
	return "词法级边"
}

// Trace 两符号间调用链（v5.9.0，MCP get_trace 数据面）：与 search_sem 同底座
// （semsearch.Manager.Trace——符号邻接图无向 BFS 最短链），目录解析复用
// workcopyDir。守卫与错误语义同 SearchSem：未启用/工作副本不可用返回 error
// （环境面故障）；不可达是正常查询结果，返回提示文本非 error。
func (e *Engine) Trace(ctx context.Context, platform, owner, repo, number, from, to string) (string, error) {
	return traceCallChain(e, ctx, platform, owner, repo, number, from, to)
}

// SymContext 单符号图上下文（v5.9.0，MCP get_symbol_context 数据面）：定义位置 +
// 直接调用方 + 被调（同名多候选合并邻接；v5.10.0 键迁移后多候选=同 bare 名跨
// 目录，候选数一并透出）。守卫/目录解析/错误语义同 SearchSem 与 Trace；未解析
// 到符号返回提示文本非 error。
func (e *Engine) SymContext(ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
	return symbolContext(e, ctx, platform, owner, repo, number, symbol)
}
