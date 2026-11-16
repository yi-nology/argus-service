// workflow_impl_r3tools_search.go —— Workflow 检索/图查询工具的包级实现。

package requirement

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/codesearch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/goagent-kernel/spill"
)

// r3SearchTool 的包级实现（转发壳见原文件）。
func r3SearchTool(w *Workflow) tool.BaseTool {
	if w.CodeSearchDir == "" {
		return nil
	}
	st, err := utils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			res, err := codesearch.Search(ctx, w.CodeSearchDir, in.Pattern,
				codesearch.Options{Globs: codesearch.NormalizeGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
			if err != nil {
				return &codesearch.ToolOut{Result: plugin.ToolErrNote(err)}, nil
			}
			return &codesearch.ToolOut{Result: DataFence("search_code 检索结果（数据）", w.stashOverflowAt(res.Render(), detailOverflowMax))}, nil
		})
	if err != nil {
		w.Log.Warn("argus.r3.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// r3DetailTool 的包级实现（转发壳见原文件）。
func r3DetailTool(w *Workflow) (tool.BaseTool, error) {
	return utils.InferTool("get_detail",
		"读取此前被截断的完整工具输出（分页）。ref 来自截断提示中的 ref 编号（如 \"d1\"）；"+
			"默认读第一页，长输出用 offset/limit 续页（返回值含 next_offset 时还有后续页）。",
		func(ctx context.Context, in *getDetailIn) (*symOut, error) {
			ref := strings.TrimSpace(in.Ref)
			if ref == "" || w.SpillDir == "" {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			// SpillDir 布局 = <root>/<taskID>：root/session 拆给上游 spill 契约
			//（净化/会话守卫/分页语义全在上游单源）。
			pg, err := spill.ReadPage(filepath.Dir(w.SpillDir), filepath.Base(w.SpillDir),
				ref, in.Offset, in.Limit)
			if err != nil {
				return &symOut{Result: "无此详情 ref（编号无效或已被后续详情挤出）"}, nil
			}
			out := DataFence("get_detail 完整输出（数据）", pg.Content)
			if !pg.EOF {
				out += fmt.Sprintf("\n…（分页取回：本页 %d/%d 字节，offset=%d；"+
					"继续用 get_detail ref=%q offset=%d 取下一页）",
					pg.Bytes, pg.Total, pg.Offset, pg.Name, pg.NextOffset)
			}
			return &symOut{Result: out}, nil
		})
}

// r3GraphTools 的包级实现（转发壳见原文件）。
func r3GraphTools(w *Workflow) (getTrace, getSymCtx tool.BaseTool, err error) {
	if w.Graph == nil {
		return nil, nil, nil
	}
	getTrace, err = utils.InferTool("get_trace",
		"查询两个 Go 符号间的最短调用链（无向 BFS，≤4 跳）——改动如何传导到下游、上游从哪里进来。返回逐跳链，每步带 file:line。",
		func(ctx context.Context, in *traceIn) (*symOut, error) {
			if w.Graph == nil {
				return &symOut{Result: "符号图未注入（本次审查不可用图查询，改用 get_callers/get_references）"}, nil
			}
			from, to := strings.TrimSpace(in.From), strings.TrimSpace(in.To)
			if from == "" || to == "" {
				return &symOut{Result: "from/to 均不能为空"}, nil
			}
			chain, found := w.Graph.Path(from, to, graphTraceHops)
			if !found {
				// 不可达/符号不在图内是正常查询结果：提示文本非 error，注明边界。
				return &symOut{Result: fmt.Sprintf(
					"未找到 %s → %s 的 ≤%d 跳调用链（%s，可改用 get_callers 查调用形态）",
					from, to, graphTraceHops, graphEdgeNote(w.Graph.Precise()))}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "get_trace（%s → %s）≤%d 跳最短调用链，%s，引用前须 get_file 核实：\n",
				from, to, graphTraceHops, graphEdgeNote(w.Graph.Precise()))
			for i, r := range chain {
				if i > 0 {
					switch r.Rel {
					case graphRelCalls:
						b.WriteString("—calls→ ")
					case graphRelCalledBy:
						b.WriteString("—called_by→ ")
					}
				}
				if r.Path != "" {
					fmt.Fprintf(&b, "%s (%s:%d)\n", r.Symbol, r.Path, r.Line)
				} else {
					fmt.Fprintf(&b, "%s\n", r.Symbol)
				}
			}
			return &symOut{Result: DataFence("get_trace 调用链（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, err
	}
	getSymCtx, err = utils.InferTool("get_symbol_context",
		"查询 Go 符号定义位置、调用方与被调全景（消歧符号图，比 get_callers 全；同名多候选合并邻接）。返回 定义 file:line + 调用方/被调列表。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			if w.Graph == nil {
				return &symOut{Result: "符号图未注入（本次审查不可用图查询，改用 get_callers/get_references）"}, nil
			}
			name := strings.TrimSpace(in.Name)
			if name == "" {
				return &symOut{Result: "name 不能为空"}, nil
			}
			self, callers, callees, cands, ok := w.Graph.SymbolContext(name)
			if !ok {
				// 未解析到符号是正常查询结果：提示文本非 error。
				return &symOut{Result: fmt.Sprintf("未在符号图内解析到符号 %q（裸名或 dir.Recv.Name 全名均可）", name)}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "get_symbol_context（symbol=%s）\n", name)
			if self.Path != "" {
				fmt.Fprintf(&b, "定义：%s (%s:%d)\n", self.Symbol, self.Path, self.Line)
			} else {
				fmt.Fprintf(&b, "定义：%s\n", self.Symbol)
			}
			// 候选数 >1 即邻接为同名合并，提示引用前逐候选核实（v5.10.0 同款语义）。
			if cands > 1 {
				fmt.Fprintf(&b, "同名候选 %d 处，邻接已合并（引用前须逐候选核实）\n", cands)
			}
			fmt.Fprintf(&b, "%s，引用前须 get_file 核实\n", graphEdgeNote(w.Graph.Precise()))
			if seg := renderGraphNeighbors(callers); seg != "" {
				fmt.Fprintf(&b, "调用方（≤%d）：%s\n", graphNeighborCap, seg)
			}
			if seg := renderGraphNeighbors(callees); seg != "" {
				fmt.Fprintf(&b, "被调（≤%d）：%s\n", graphNeighborCap, seg)
			}
			return &symOut{Result: DataFence("get_symbol_context 图上下文（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, err
	}
	return getTrace, getSymCtx, nil
}

// r3SemSearchTool 的包级实现（转发壳见原文件）。
func r3SemSearchTool(w *Workflow) tool.BaseTool {
	if w.Graph == nil || !w.Graph.Searchable() {
		return nil
	}
	st, err := utils.InferTool("search_sem",
		"审查面语义检索：按意图/自然语言找代码（词法 search_code 答不了的『哪里处理 X』——代码里未必含查询词）。命中是摘要线索引用，须 get_file 核实后再下判定；符号命中附调用邻接（调用方/被调）。返回按相似度降序的 file:line+符号+摘要列表。",
		func(ctx context.Context, in *semSearchIn) (*symOut, error) {
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return &symOut{Result: "query 不能为空"}, nil
			}
			topK := in.TopK
			if topK <= 0 {
				topK = graphSemDefaultTopK
			}
			if topK > graphSemMaxTopK {
				topK = graphSemMaxTopK
			}
			hits, err := w.Graph.Search(ctx, query, topK, 0)
			if err != nil {
				return nil, err // 检索失败（环境面故障）上抛，与无命中（正常结果）区分
			}
			if len(hits) == 0 {
				return &symOut{Result: "语义检索无命中（可换更具体的意图描述，或改用 search_code 正则检索）"}, nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "search_sem（query=%s）命中 %d 条，按相似度降序——命中是摘要线索，引用前须 get_file 核实：\n", query, len(hits))
			for i, h := range hits {
				text := h.Text
				if rc := utf8.RuneCountInString(text); rc > graphSemHitTextCap {
					var t string
					t, _ = textutil.TruncRunes(text, graphSemHitTextCap)
					text = t + "…"
				}
				symbol := ""
				if h.Symbol != "" {
					symbol = " " + h.Symbol
				}
				fmt.Fprintf(&b, "\n[%d] %s:%d%s score=%.3f\n%s\n", i+1, h.Path, h.Line, symbol, h.Score, text)
				// 图增强命中（Symbol 非空底座自动附邻接）：调用方/被调分组渲染，
				// 单侧封顶 graphSemAdjCap（邻接 SymRef 无 loc，只显符号名）。
				if h.Symbol != "" && len(h.Neighbors) > 0 {
					if seg := renderSemAdjacency(h.Neighbors, graphSemAdjCap); seg != "" {
						fmt.Fprintf(&b, "邻接：%s\n", seg)
					}
				}
			}
			return &symOut{Result: DataFence("search_sem 检索结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		w.Log.Warn("argus.r3.search_sem_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}
