// writing_fixverify.go —— 修复验证环与修复分支命名空间（自 writing.go 拆出）。

package runner

import (
	"context"
	"strings"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// verifyFixes 修复验证环（v3.74.0）：fixverifier 对带 suggested_fix 的意见做
// 沙箱应用+编译验证并回写 f.Verified。未配置包装命令 / 无工作副本 / 全失败
// 均静默跳过——功能是加分项，永不阻断审查主链路。
func (r *Runner) verifyFixes(ctx context.Context, task *storage.Task, pr *plugin.PRContext, out *plugin.WorkflowOutput) {
	verifyFixes(r, ctx, task, pr, out)
}

// fixBranchPrefix 修复分支命名空间（fixBranchName 与防循环护栏共用单源）。
const fixBranchPrefix = "argus/fix/"

// fixBranchName 修复分支名（v7.0 Verified Fix Loop）：argus/fix/<taskID>。
// argus/ 前缀自有命名空间与常见保护分支（main/master/release/*）无交集——
// branch 形态任何失败都不可能触碰保护分支（阶段 2 验收线）。
func fixBranchName(pr *plugin.PRContext) string { return fixBranchPrefix + pr.Number }

// isFixBranch 源分支是否属于修复分支命名空间（防循环护栏判定）。
func isFixBranch(branch string) bool { return strings.HasPrefix(branch, fixBranchPrefix) }
