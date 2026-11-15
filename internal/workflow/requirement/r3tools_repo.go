// r3tools_repo.go —— 仓库内容工具：get_file（行号/数据区/两级化）、
// get_issue、get_diff。

package requirement

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
)

// r3RepoTools 仓库内容三工具：get_file（行号前缀 + 大输出两级化 + 数据区）、
// get_issue（标题正文合并数据区）、get_diff（截断重取）。
func (w *Workflow) r3RepoTools(ctx context.Context, task plugin.Task, pr plugin.PRContext,
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
