// r3tools_graph.go —— 消歧符号图工具（get_trace/get_symbol_context）与
// search_sem 语义检索工具，含图面边界措辞与邻接渲染。

package requirement

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/semsearch"
)

// ---------- 消歧符号图工具（v5.11.0 纯图模式） ----------

// 图边向词表（semsearch.SymRef.Rel；semsearch 不导出关系词表，与 qa 包同款
// 手写单点，改 semsearch 词表须同步两处）。
const (
	graphRelCalls    = "calls"     // 本步调用下一步
	graphRelCalledBy = "called_by" // 本步被下一步调用（无向 BFS 逆向步）
	graphRelCaller   = "caller"    // 邻接面：调用方（search_sem 命中附）
	graphRelCallee   = "callee"    // 邻接面：被调（search_sem 命中附）
)

// search_sem 审查面的入参钳制与渲染预算（qa.SearchSem MCP 面同值手写单点，
// 改此须同步两处；工具描述「缺省 5、最大 10」与本值同源）。
const (
	graphSemDefaultTopK = 5
	graphSemMaxTopK     = 10
	graphSemHitTextCap  = 600 // 单条命中摘要渲染截断（rune）
	graphSemAdjCap      = 3   // 命中邻接单侧封顶（调用方 3 + 被调 3，合计 ≤6）
)

// graphTraceHops get_trace 跳数（与 semsearch graphHopsDefault 同值手写，改此须同步）。
const graphTraceHops = 4

// graphNeighborCap get_symbol_context 调用方/被调单侧文案上限（底座
// graphCallersCap/graphCalleesCap 同值——底座已封顶，此处只管渲染文案）。
const graphNeighborCap = 8

// graphEdgeNote 图面边界措辞随精确边模式切换（precise=Index.Precise()）：
// ARGUS_GRAPH_PRECISE 部署开启且成功应用精确边时为 go/types 类型解析级
// （接口分派/方法值不可见），否则词法级。get_trace/get_symbol_context 提示共用
// （qa.edgeQualityNote 同款措辞的 requirement 包内单点）。
func graphEdgeNote(precise bool) string {
	if precise {
		return "类型解析级边（go/types；接口分派/方法值不可见）"
	}
	return "词法级边（接口分派/方法值不可见）"
}

// r3GraphTools 消歧符号图工具对（v5.11.0 纯图模式）：get_trace 两符号最短调用
// 链 + get_symbol_context 单符号全景。w.Graph 为 nil（图索引未注入：codegraph
// 关闭/工作副本不可用/图构建失败降级）时返回 nil（不进表）——与 CodeGraph
// 四件套独立判空互不绑定；降级文案为提示文本非 error（查无结果是正常查询结果）。
func (w *Workflow) r3GraphTools() (getTrace, getSymCtx tool.BaseTool, err error) {
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

// renderGraphNeighbors 图邻接单行渲染（get_symbol_context 调用方/被调用）：有
// 定义位置带 (file:line)，无位置（预算截断挤出 loc 的边残留）只显符号；「、」分隔。
func renderGraphNeighbors(refs []semsearch.SymRef) string {
	if len(refs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		if r.Path != "" {
			parts = append(parts, fmt.Sprintf("%s (%s:%d)", r.Symbol, r.Path, r.Line))
		} else {
			parts = append(parts, r.Symbol)
		}
	}
	return strings.Join(parts, "、")
}

// ---------- search_sem 语义检索工具（v6.1.0） ----------

// semSearchIn search_sem 入参（TopK 缺省 5、上界 10，执行侧钳制）。
type semSearchIn struct {
	Query string `json:"query" jsonschema:"description=意图/自然语言查询（如 哪里处理认证 哪里刷新 session token）"`
	TopK  int    `json:"topK,omitempty" jsonschema:"description=返回条数上限（缺省 5，最大 10）"`
}

// r3SemSearchTool search_sem 语义检索工具（v6.1.0，审查 ReAct 面）：按意图/
// 自然语言找代码——结构化摘要 embedding + 进程内余弦 + 命中附调用邻接，补
// search_code（词法正则）答不了的「哪里处理 X」类问题（代码里未必含查询词）。
// 挂载条件 w.Graph 非 nil 且 Searchable()（embedder 随 ARGUS_SEMSEARCH opt-in
// 门注入，纯图模式只挂 get_trace/get_symbol_context 不挂本工具——挂出必然报
// 「语义检索未启用」的死工具没有意义）。与 MCP 面 search_sem（qa.SearchSem
// 数据面）同名不冲突：不同执行面，本工具直接查任务已建好的 Workflow.Graph。
// 渲染与错误语义仿 qa.SearchSem：无命中返回提示文本非 error（正常查询结果），
// 检索失败 error 上抛（环境面故障）。
func (w *Workflow) r3SemSearchTool() tool.BaseTool {
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

// renderSemAdjacency search_sem 命中邻接单行渲染（qa.renderSemNeighbors 同款
// 视觉的 requirement 包内单点）：按 Rel 分「调用方/被调」两组各取前 limit 个，
// 组内顿号连接、组间分号连接——`调用方 A、B；被调 C`；邻接 SymRef 无定义位置
// （neighborsOf 不带 loc），只显符号名；空组省略，两组皆空返回空串。
func renderSemAdjacency(ns []semsearch.SymRef, limit int) string {
	segment := func(label string, refs []semsearch.SymRef) string {
		if len(refs) == 0 {
			return ""
		}
		parts := make([]string, 0, len(refs))
		for _, r := range refs {
			parts = append(parts, r.Symbol)
		}
		return label + " " + strings.Join(parts, "、")
	}
	var callers, callees []semsearch.SymRef
	for _, r := range ns {
		switch r.Rel {
		case graphRelCaller:
			if len(callers) < limit {
				callers = append(callers, r)
			}
		case graphRelCallee:
			if len(callees) < limit {
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
