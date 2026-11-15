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
	return r3Tools(w, ctx, task, spec)
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
	return r3PriorityTable(w, ctx, spec, tl)
}

// ReviewTools R4 复用：审查工具表（get_file/get_diff/知识/技能/外部 MCP；
// spec 传 nil 自动跳过 AC 判定专属的 get_issue，When 文案切审查语境）。
// 与 R3 共用同一套工具基础设施——diffprep 清洗、数据区围栏、限流包装全部一致。
func (w *Workflow) ReviewTools(ctx context.Context, task plugin.Task) ([]tool.BaseTool, string, error) {
	return reviewTools(w, ctx, task)
}
