package qa

import (
	"context"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/toolgate/toolprior"

	"github.com/yi-nology/argus/internal/plugin"
)

// buildTools 实现 Engine.tools（Q&A 工具表装配）。
func buildTools(e *Engine, ctx context.Context, platform, owner, repo, number string) ([]tool.BaseTool, string, error) {
	getDiff, getFile, err := e.diffFileTools(ctx, platform, owner, repo, number)
	if err != nil {
		return nil, "", err
	}
	similarIssue, err := e.similarIssueTool(platform, owner, repo)
	if err != nil {
		return nil, "", err
	}

	// search_code（v3.91.0）：工作副本全文检索（与 R3/R4 审查面同款纪律）。
	// v3.91.1 起惰性解析——目录就绪（含冷克隆，30s 预算）推迟到工具首调，
	// 不需要检索的提问不付任何克隆成本。
	var searchCode tool.BaseTool
	if e.WC != nil && e.CodeSearch {
		searchCode = e.searchCodeTool(platform, owner, repo, number)
	}

	table := toolprior.NewTable()
	table.Add(toolprior.Entry{Tool: getDiff, Priority: toolprior.PriorityCore,
		When: "回答涉及具体代码改动的问题前先通读", Cost: "low"})
	table.Add(toolprior.Entry{Tool: getFile, Priority: toolprior.PriorityCore,
		When: "核实 diff 看不到的调用方/实现细节时使用", Cost: "low"})
	if searchCode != nil {
		table.Add(toolprior.Entry{Tool: searchCode, Priority: toolprior.PriorityCore,
			When: "跨文件找调用方/相似实现/字符串或配置线索（get_file 只能按路径读）时使用", Cost: "low"})
	}
	if similarIssue != nil {
		table.Add(toolprior.Entry{Tool: similarIssue, Priority: toolprior.PrioritySupport,
			When: "用户问『有没有类似问题/是否已知』时检索历史 issue", Cost: "low"})
	}
	if e.Knowledge != nil {
		if kt := e.Knowledge.AsTool(); kt != nil {
			table.Add(toolprior.Entry{Tool: kt, Priority: toolprior.PrioritySupport,
				When: "涉及团队规范/历史结论时检索", Cost: "medium"})
		}
	}
	strategy := table.StrategyPrompt(ctx)
	return table.Ordered(ctx), strategy, nil
}

// buildDiffFileTools 实现 Engine.diffFileTools（get_diff + get_file 核心工具）。
func buildDiffFileTools(e *Engine, ctx context.Context, platform, owner, repo, number string) (tool.BaseTool, tool.BaseTool, error) {
	getDiff, err := toolutils.InferTool("get_diff",
		"获取本 PR 的完整 diff 文本（回答涉及具体代码改动的问题时使用）。",
		func(ctx context.Context, _ *plugin.ToolDiffIn) (*plugin.ToolDiffOut, error) {
			md, err := e.SDK.GetCRDiff(ctx, platform, owner, repo, number)
			if err != nil {
				return nil, err
			}
			text := ""
			if md != nil {
				text = md.RawDiff
			}
			text = textutil.TruncNote(text, 30_000, "截断")
			return &plugin.ToolDiffOut{Diff: plugin.DataFence(ctx, "get_diff（数据）", text)}, nil
		})
	if err != nil {
		return nil, nil, err
	}

	getFile, err := toolutils.InferTool("get_file",
		"读取仓库某文件在 PR head 提交时的完整内容（核实 diff 看不到的实现上下文）。参数 path 为仓库相对路径。返回内容每行带行号前缀（行号|内容），引用证据 file:line 时使用该行号。",
		func(ctx context.Context, in *plugin.ToolFileIn) (*plugin.ToolFileOut, error) {
			ref := ""
			if cr, err := e.SDK.GetCR(ctx, platform, owner, repo, number); err == nil && cr != nil {
				ref = cr.HeadSHA
			}
			content, ferr := e.SDK.GetFileContent(ctx, platform, owner, repo, in.Path, ref)
			if ferr != nil {
				return &plugin.ToolFileOut{Error: plugin.ToolErrNote(ferr)}, nil // 工具错误回注（提醒信封），不中断 ReAct
			}
			// 逐行加行号（深挖 P0）：回答涉及代码行为时要求 file:line 证据，
			// 无行号原文等于逼模型凭空编行号。行号前缀先于截断。
			content = textutil.NumberLines(content)
			if rc := utf8.RuneCountInString(content); rc > 20_000 {
				var trunc string
				trunc, _ = textutil.TruncRunes(content, 20_000)
				content = trunc + "\n…（截断）"
			}
			return &plugin.ToolFileOut{Content: plugin.DataFence(ctx, "get_file 文件内容（数据）", content)}, nil
		})
	if err != nil {
		return nil, nil, err
	}
	return getDiff, getFile, nil
}
