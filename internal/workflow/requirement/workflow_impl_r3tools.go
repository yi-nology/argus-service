// workflow_impl_r3tools.go —— Workflow.R3/R4 工具装配的包级实现。

package requirement

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/toolgate/toolprior"
	"github.com/yi-nology/argus/internal/langcap"
	"github.com/yi-nology/argus/internal/plugin"
)

// r3Tools 的包级实现（转发壳见原文件）。
func r3Tools(w *Workflow, ctx context.Context, task plugin.Task,
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

// r3PriorityTable 的包级实现（转发壳见原文件）。
func r3PriorityTable(w *Workflow, ctx context.Context, spec *plugin.RequirementSpec,
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

// ReviewTools 的包级实现（转发壳见原文件）。
func reviewTools(w *Workflow, ctx context.Context, task plugin.Task) ([]tool.BaseTool, string, error) {
	return w.r3Tools(ctx, task, nil)
}

// r3AddExternalTools 的包级实现（转发壳见原文件）。
func r3AddExternalTools(w *Workflow, ctx context.Context, table *toolprior.Table) {
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

// r3CodeGraphTools 的包级实现（转发壳见原文件）。
func r3CodeGraphTools(w *Workflow) (getSymbol, getRefs, getCallers, getPoly tool.BaseTool, err error) {
	if w.CodeGraph == nil {
		return nil, nil, nil, nil, nil
	}
	getSymbol, err = utils.InferTool("get_symbol",
		"按名称查询 Go 符号（函数/方法/类型/变量）的定义位置与签名，用于核实 diff 中看不到的实现细节。返回 file:line + 签名列表。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.Definitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				name := d.Name
				if d.Recv != "" {
					name = d.Recv + "." + d.Name
				}
				fmt.Fprintf(&b, "%s:%d  %s %s\n", d.File, d.Line, name, d.Signature)
			}
			return &symOut{Result: DataFence("get_symbol 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getRefs, err = utils.InferTool("get_references",
		"查询 Go 标识符在仓库中的出现位置（file:line 列表），用于评估跨文件影响面（谁调用了被改的函数/谁用了被改的类型）。精确大小写匹配。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.References(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到出现位置（可能仅定义处或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_references 结果（数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getCallers, err = utils.InferTool("get_callers",
		"查询 Go 函数的调用形态出现位置（pkg.F( / F( 词法匹配，file:line 列表）——评估改动影响面优先用本工具；词法级索引，引用前须用 get_file 核实。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			refs := w.CodeGraph.GoCallers(in.Name, 50)
			if len(refs) == 0 {
				return &symOut{Result: "未找到调用形态出现位置（可能未被调用或不在索引范围）"}, nil
			}
			var b strings.Builder
			for _, r := range refs {
				fmt.Fprintf(&b, "%s:%d\n", r.File, r.Line)
			}
			return &symOut{Result: DataFence("get_callers 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	getPoly, err = utils.InferTool("get_poly_symbol",
		"按名称查询 "+langcap.JoinLangs(langcap.PolySymbolExts)+" 函数的定义位置与参数（词法级索引），用于核实 diff 中看不到的非 Go 实现。",
		func(ctx context.Context, in *symIn) (*symOut, error) {
			defs := w.CodeGraph.PolyDefinitions(in.Name)
			if len(defs) == 0 {
				return &symOut{Result: "未找到符号 " + in.Name}, nil
			}
			var b strings.Builder
			for _, d := range defs {
				if d.Doc != "" {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)  # %s\n", d.File, d.Line, d.Name, d.Args, d.Doc)
				} else {
					fmt.Fprintf(&b, "%s:%d  def %s(%s)\n", d.File, d.Line, d.Name, d.Args)
				}
			}
			return &symOut{Result: DataFence("get_poly_symbol 结果（词法级，数据）", w.stashOverflowAt(b.String(), detailOverflowMax))}, nil
		})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return getSymbol, getRefs, getCallers, getPoly, nil
}

// r3RepoTools 的包级实现（转发壳见原文件）。
func r3RepoTools(w *Workflow, ctx context.Context, task plugin.Task, pr plugin.PRContext,
	fileMax, issueMax, diffMax int) (getFile, getIssue, getDiff tool.BaseTool, err error) {
	getFile, err = utils.InferTool("get_file",
		plugin.ToolGetFileDesc,
		func(ctx context.Context, in *plugin.ToolFileIn) (*plugin.ToolFileOut, error) {
			content, err := w.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo, in.Path, pr.HeadSHA)
			if err != nil {
				return &plugin.ToolFileOut{Error: plugin.ToolErrNote(err)}, nil // 工具错误回注文本（提醒信封），不中断 ReAct
			}
			// 逐行加行号（深挖 P0）：证据门要求 file:line 定位，而无行号的原文等于
			// 逼模型凭空编行号。行号前缀先于截断，保证已见行号真实可查。
			content = textutil.NumberLines(content)
			// 大输出两级化（v3.39.0）：超限全文入任务级详情库（get_detail 取回），
			// 进上下文的只留上限内正文 + 取回指引
			content = w.stashOverflowAt(content, fileMax)
			// 文件内容是 PR 作者完全可控的注入面：回注 agent 前必须包数据区（防线 #1）
			return &plugin.ToolFileOut{Content: DataFence("get_file 文件内容（数据）", content)}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}

	getIssue, err = utils.InferTool("get_issue",
		"读取本仓库某个 issue 的标题与正文（补充需求上下文）。",
		func(ctx context.Context, in *getIssueIn) (*getIssueOut, error) {
			issue, err := w.SDK.GetIssue(ctx, pr.Platform, pr.Owner, pr.Repo, in.Number)
			if err != nil {
				return &getIssueOut{Error: plugin.ToolErrNote(err)}, nil
			}
			body := textutil.TruncNote(issue.Body, issueMax, "截断")
			// 标题+正文一起进数据区（两者都攻击者可控）
			return &getIssueOut{Body: DataFence("get_issue 标题与正文（数据）", issue.Title+"\n"+body)}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}

	getDiff, err = utils.InferTool("get_diff",
		"重新获取本次 PR 的清洗后 diff 文本（当上下文中 diff 被截断时使用）。",
		func(ctx context.Context, _ *plugin.ToolDiffIn) (*plugin.ToolDiffOut, error) {
			if task.Diff != nil {
				text := task.Diff.Text
				text = textutil.TruncNote(text, diffMax, "截断")
				return &plugin.ToolDiffOut{Diff: DataFence("get_diff（数据）", text)}, nil
			}
			return &plugin.ToolDiffOut{Diff: ""}, nil
		})
	if err != nil {
		return nil, nil, nil, err
	}
	return getFile, getIssue, getDiff, nil
}
