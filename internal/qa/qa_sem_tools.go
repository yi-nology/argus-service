package qa

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/plugin"
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
	if e.SemSearch == nil || !e.SemSearchOn {
		return "", fmt.Errorf("search_sem 未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）")
	}
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query 为空")
	}
	if topK <= 0 {
		topK = semSearchDefaultTopK
	}
	if topK > semSearchMaxTopK {
		topK = semSearchMaxTopK
	}
	dir, ok := e.workcopyDir(ctx, platform, owner, repo, number)
	if !ok {
		return "", fmt.Errorf("工作副本不可用（冷克隆超时或失败），可稍后重试或改用 search_code/get_file")
	}
	hits, err := e.SemSearch.Search(ctx, dir, query, topK, 0)
	if err != nil {
		return "", fmt.Errorf("语义检索失败: %w", err)
	}
	if len(hits) == 0 {
		return "语义检索无命中（可换更具体的意图描述，或改用 search_code 正则检索）", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "search_sem（query=%s）命中 %d 条，按相似度降序——命中是摘要线索，引用前须 get_file 核实：\n", query, len(hits))
	for i, h := range hits {
		text := h.Text
		if rc := utf8.RuneCountInString(text); rc > semHitTextCap {
			var t string
			t, _ = textutil.TruncRunes(text, semHitTextCap)
			text = t + "…"
		}
		symbol := ""
		if h.Symbol != "" {
			symbol = " " + h.Symbol
		}
		fmt.Fprintf(&b, "\n[%d] %s:%d%s score=%.3f\n%s\n", i+1, h.Path, h.Line, symbol, h.Score, text)
		// v5.9.0 图增强：符号命中附调用方/被调邻接行（底座自动附，单侧封顶 3）。
		if h.Symbol != "" && len(h.Neighbors) > 0 {
			fmt.Fprintf(&b, "邻接：%s\n", renderSemNeighbors(h.Neighbors, semSearchNeighborCap))
		}
	}
	return plugin.DataFence("search_sem 检索结果（数据）", b.String()), nil
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
	if e.SemSearch == nil || !e.SemSearchOn {
		return "", fmt.Errorf("get_trace 未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）")
	}
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if from == "" || to == "" {
		return "", fmt.Errorf("from/to 为空")
	}
	dir, ok := e.workcopyDir(ctx, platform, owner, repo, number)
	if !ok {
		return "", fmt.Errorf("工作副本不可用（冷克隆超时或失败），可稍后重试或改用 search_code/get_file")
	}
	chain, found, err := e.SemSearch.Trace(ctx, dir, from, to)
	if err != nil {
		return "", fmt.Errorf("调用链查询失败: %w", err)
	}
	if !found {
		return fmt.Sprintf("未找到 %s → %s 的 ≤%d 跳调用链（%s：接口分派不可见，可改用 search_code 查调用形态）", from, to, traceHops, edgeQualityNote(e.SemSearch.Precise(dir))), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "get_trace（%s → %s）≤%d 跳最短调用链，%s引用前须 get_file 核实：\n", from, to, traceHops, edgeQualityNote(e.SemSearch.Precise(dir)))
	for i, r := range chain {
		if i > 0 {
			switch r.Rel {
			case semRelCalls:
				b.WriteString("—calls→ ")
			case semRelCalledBy:
				b.WriteString("—called_by→ ")
			}
		}
		if r.Path != "" {
			fmt.Fprintf(&b, "%s (%s:%d)\n", r.Symbol, r.Path, r.Line)
		} else {
			fmt.Fprintf(&b, "%s\n", r.Symbol)
		}
	}
	return plugin.DataFence("get_trace 调用链（数据）", b.String()), nil
}

// SymContext 单符号图上下文（v5.9.0，MCP get_symbol_context 数据面）：定义位置 +
// 直接调用方 + 被调（同名多候选合并邻接；v5.10.0 键迁移后多候选=同 bare 名跨
// 目录，候选数一并透出）。守卫/目录解析/错误语义同 SearchSem 与 Trace；未解析
// 到符号返回提示文本非 error。
func (e *Engine) SymContext(ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
	if e.SemSearch == nil || !e.SemSearchOn {
		return "", fmt.Errorf("get_symbol_context 未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）")
	}
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return "", fmt.Errorf("symbol 为空")
	}
	dir, ok := e.workcopyDir(ctx, platform, owner, repo, number)
	if !ok {
		return "", fmt.Errorf("工作副本不可用（冷克隆超时或失败），可稍后重试或改用 search_code/get_file")
	}
	self, in, out, cands, found, err := e.SemSearch.SymbolContext(ctx, dir, symbol)
	if err != nil {
		return "", fmt.Errorf("符号上下文查询失败: %w", err)
	}
	if !found {
		return fmt.Sprintf("未在索引内解析到符号 %q（裸名或 Recv.Name 全名均可）", symbol), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "get_symbol_context（symbol=%s）\n", symbol)
	if self.Path != "" {
		fmt.Fprintf(&b, "定义：%s (%s:%d)\n", self.Symbol, self.Path, self.Line)
	} else {
		fmt.Fprintf(&b, "定义：%s\n", self.Symbol)
	}
	// v5.10.0：候选数 >1 即邻接为跨目录同名合并，提示引用前逐候选核实。
	if cands > 1 {
		fmt.Fprintf(&b, "同名候选 %d 处，邻接已合并（引用前须逐候选核实）\n", cands)
	}
	// v5.10.0：边界声明随精确边模式动态化（get_trace 围栏首行同款）。
	fmt.Fprintf(&b, "%s，引用前须 get_file 核实\n", edgeQualityNote(e.SemSearch.Precise(dir)))
	// 单组列表过 renderSemNeighbors 只会出该组标签，剥去避免与行标签重复。
	if seg := renderSemNeighbors(in, symContextCap); seg != "" {
		fmt.Fprintf(&b, "调用方（≤%d）：%s\n", symContextCap, strings.TrimPrefix(seg, "调用方 "))
	}
	if seg := renderSemNeighbors(out, symContextCap); seg != "" {
		fmt.Fprintf(&b, "被调（≤%d）：%s\n", symContextCap, strings.TrimPrefix(seg, "被调 "))
	}
	return plugin.DataFence("get_symbol_context 图上下文（数据）", b.String()), nil
}
