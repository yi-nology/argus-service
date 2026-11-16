// r3tools_repo.go —— 仓库内容工具：get_file（行号/数据区/两级化）、
// get_issue、get_diff。

package requirement

import (
	"context"

	"github.com/cloudwego/eino/components/tool"

	"github.com/yi-nology/argus/internal/plugin"
)

// r3RepoTools 仓库内容三工具：get_file（行号前缀 + 大输出两级化 + 数据区）、
// get_issue（标题正文合并数据区）、get_diff（截断重取）。
func (w *Workflow) r3RepoTools(ctx context.Context, task plugin.Task, pr plugin.PRContext,
	fileMax, issueMax, diffMax int) (getFile, getIssue, getDiff tool.BaseTool, err error) {
	return r3RepoTools(w, ctx, task, pr, fileMax, issueMax, diffMax)
}
