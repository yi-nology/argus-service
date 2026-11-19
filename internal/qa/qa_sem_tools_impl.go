package qa

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
)

// searchSem 实现 Engine.SearchSem（MCP search_sem 数据面）。
func searchSem(e *Engine, ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error) {
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
	return plugin.DataFence(ctx, "search_sem 检索结果（数据）", b.String()), nil
}

// traceCallChain 实现 Engine.Trace（MCP get_trace 数据面）。
func traceCallChain(e *Engine, ctx context.Context, platform, owner, repo, number, from, to string) (string, error) {
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
	return plugin.DataFence(ctx, "get_trace 调用链（数据）", b.String()), nil
}

// symbolContext 实现 Engine.SymContext（MCP get_symbol_context 数据面）。
func symbolContext(e *Engine, ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
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
	return plugin.DataFence(ctx, "get_symbol_context 图上下文（数据）", b.String()), nil
}
