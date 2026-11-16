// r3tools_graph.go —— 消歧符号图工具（get_trace/get_symbol_context）与
// search_sem 语义检索工具，含图面边界措辞与邻接渲染。

package requirement

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"

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
	return r3GraphTools(w)
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
	return r3SemSearchTool(w)
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
