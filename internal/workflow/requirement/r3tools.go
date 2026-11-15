// r3tools.go —— R3 工具装配面：工具表编排 + 优先级表组装 + R4 复用入口
//（各工具族见 r3tools_repo/r3tools_codegraph/r3tools_graph/r3tools_retrieval/
// r3tools_external，wire 类型见 r3tools_types）。

package requirement

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/toolgate/toolprior"

	"github.com/yi-nology/argus/internal/plugin"
)

// r3Tools R3 的工具表（每个任务构造一次，闭包绑定任务上下文）。
// 经 toolprior 表组织：优先级排序 + 策略提示 + 外部工具调用限制。
// v3.55.0 拆分：核心仓库工具/CodeGraph 工具/getDetail/外部工具各自成方法；
// 批 D 波2 再拆：限额缺省（r3CharLimits）与优先级表组装（r3PriorityTable）
// 各自成 helper，本函数只做编排。
func (w *Workflow) r3Tools(ctx context.Context, task plugin.Task,
	spec *plugin.RequirementSpec) ([]tool.BaseTool, string, error) {
	pr := task.PR
	fileMax, issueMax, diffMax := r3CharLimits(w.GetFileMaxChars, w.GetIssueMaxChars, w.GetDiffMaxChars)

	getFile, getIssue, getDiff, err := w.r3RepoTools(ctx, task, pr, fileMax, issueMax, diffMax)
	if err != nil {
		return nil, "", err
	}
	getSymbol, getRefs, getCallers, getPoly, err := w.r3CodeGraphTools()
	if err != nil {
		return nil, "", err
	}
	getTrace, getSymCtx, err := w.r3GraphTools()
	if err != nil {
		return nil, "", err
	}
	searchSem := w.r3SemSearchTool()

	table, err := w.r3PriorityTable(ctx, spec, r3Toolset{
		getFile: getFile, getIssue: getIssue, getDiff: getDiff,
		getSymbol: getSymbol, getRefs: getRefs, getCallers: getCallers, getPoly: getPoly,
		getTrace: getTrace, getSymCtx: getSymCtx, searchSem: searchSem,
	})
	if err != nil {
		return nil, "", err
	}
	w.r3AddExternalTools(ctx, table)

	strategy := table.StrategyPrompt(ctx)
	// 循环异常守卫（v3.37.0）：整表套连续同参调用拦截——ReAct 死循环的唯一防线
	return wrapAnomalyGuard(ctx, newAnomalyGuard(), table.Ordered(ctx)), strategy, nil
}

// r3CharLimits 工具输出限额缺省：配置项 ≤0 时回落兜底值（文件 20k/issue 8k/
// diff 30k 字符），保证截断面确定。
func r3CharLimits(fileMax, issueMax, diffMax int) (int, int, int) {
	if fileMax <= 0 {
		fileMax = 20_000
	}
	if issueMax <= 0 {
		issueMax = 8_000
	}
	if diffMax <= 0 {
		diffMax = 30_000
	}
	return fileMax, issueMax, diffMax
}

// r3Toolset r3Tools 构造出的核心工具集合（优先级表装配的入参；字段名即原局部
// 工具变量名，nil 表示该工具未构造/不进表）。
type r3Toolset struct {
	getFile, getIssue, getDiff              tool.BaseTool
	getSymbol, getRefs, getCallers, getPoly tool.BaseTool
	getTrace, getSymCtx, searchSem          tool.BaseTool
}

// r3PriorityTable 优先级表组装：三层决策约束（指引/排序/限制），见
// oagentkit/toolgate/toolprior——核心工具按条件暴露（无 issue 引用不进
// get_issue、CodeGraph 关闭不进符号工具），支撑档挂 knowledge/skill/get_detail；
// 入表顺序即表内稳定序（同优先级下保持此处追加序）。
func (w *Workflow) r3PriorityTable(ctx context.Context, spec *plugin.RequirementSpec,
	tl r3Toolset) (*toolprior.Table, error) {
	// ---- 优先级表：三层决策约束（指引/排序/限制），见 oagentkit/toolgate/toolprior ----
	table := toolprior.NewTable()
	getFileWhen := "核实 AC 证据的唯一来源：每条 implemented/partial 判定前必查"
	if spec == nil {
		getFileWhen = "核实 diff 看不到的实现上下文（调用方/边界条件/历史约定）时使用"
	}
	table.Add(toolprior.Entry{Tool: tl.getFile, Priority: toolprior.PriorityCore,
		When: getFileWhen, Cost: "low"})
	// 条件暴露：需求未引用 issue 时 get_issue 是死工具，不进表（省模型注意力）
	if spec == nil || len(spec.IssueRefs) > 0 {
		table.Add(toolprior.Entry{Tool: tl.getIssue, Priority: toolprior.PriorityCore,
			When: "需求源引用了 issue，核对原始需求措辞时使用", Cost: "low"})
	}
	table.Add(toolprior.Entry{Tool: tl.getDiff, Priority: toolprior.PriorityCore,
		When: "变更概览，判定前先通读", Cost: "low"})
	if tl.getSymbol != nil && tl.getRefs != nil {
		table.Add(toolprior.Entry{Tool: tl.getSymbol, Priority: toolprior.PriorityCore,
			When: "需要核实 diff 中看不到的符号定义/签名（跨文件实现）时使用", Cost: "low"})
		table.Add(toolprior.Entry{Tool: tl.getRefs, Priority: toolprior.PriorityCore,
			When: "评估被改符号的跨文件影响面（调用方/使用点）时使用", Cost: "low"})
	}
	if tl.getCallers != nil {
		table.Add(toolprior.Entry{Tool: tl.getCallers, Priority: toolprior.PriorityCore,
			When: "Go 函数改动前先查调用形态分布（词法级，比 get_references 更聚焦调用）", Cost: "low"})
	}
	if tl.getPoly != nil {
		table.Add(toolprior.Entry{Tool: tl.getPoly, Priority: toolprior.PriorityCore,
			When: "diff 涉及 Python/JS/TS 函数且需核实定义/参数时使用", Cost: "low"})
	}
	if tl.getTrace != nil {
		table.Add(toolprior.Entry{Tool: tl.getTrace, Priority: toolprior.PriorityCore,
			When: "查两符号间调用链路（改动如何传导/上游从哪进来）", Cost: "low"})
	}
	if tl.getSymCtx != nil {
		table.Add(toolprior.Entry{Tool: tl.getSymCtx, Priority: toolprior.PriorityCore,
			When: "查符号定义+调用方+被调全景（消歧后，比 get_callers 全）", Cost: "low"})
	}
	if tl.searchSem != nil {
		table.Add(toolprior.Entry{Tool: tl.searchSem, Priority: toolprior.PriorityCore,
			When: "按意图/自然语言找代码（跨文件语义线索，grep 答不了的哪里处理 X）时使用", Cost: "low"})
	}
	if st := w.r3SearchTool(); st != nil {
		table.Add(toolprior.Entry{Tool: st, Priority: toolprior.PriorityCore,
			When: "跨文件找调用方/相似实现/字符串或配置线索（符号定位之外的词法线索，含非 Go 文件）时使用", Cost: "low"})
	}
	if w.Knowledge != nil {
		if kt := w.Knowledge.AsTool(); kt != nil {
			table.Add(toolprior.Entry{Tool: kt, Priority: toolprior.PrioritySupport,
				When: "需引用团队编码规范/部署约定作判据时使用", Cost: "medium"})
		}
	}
	if w.SkillTool != nil {
		// 决策使用模式：提示词只带可用清单（runner.skillText 返回摘要），
		// 全文由 agent 按相关性经 use_skill 工具按需加载
		table.Add(toolprior.Entry{Tool: w.SkillTool, Priority: toolprior.PrioritySupport,
			When: "任务与可用清单中某条方法论相关时加载全文", Cost: "low"})
	}

	// get_detail（v3.39.0 大输出两级化）：被截断工具的全文取回通道
	getDetail, err := w.r3DetailTool()
	if err != nil {
		return nil, err
	}
	table.Add(toolprior.Entry{Tool: getDetail, Priority: toolprior.PrioritySupport,
		When: "需要被截断工具输出的完整内容（ref 在截断提示中）时使用", Cost: "low"})
	return table, nil
}

// ReviewTools R4 复用：审查工具表（get_file/get_diff/知识/技能/外部 MCP；
// spec 传 nil 自动跳过 AC 判定专属的 get_issue，When 文案切审查语境）。
// 与 R3 共用同一套工具基础设施——diffprep 清洗、数据区围栏、限流包装全部一致。
func (w *Workflow) ReviewTools(ctx context.Context, task plugin.Task) ([]tool.BaseTool, string, error) {
	return w.r3Tools(ctx, task, nil)
}
