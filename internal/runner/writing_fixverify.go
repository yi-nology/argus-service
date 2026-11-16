// writing_fixverify.go —— 修复验证环与修复分支命名空间（自 writing.go 拆出）。

package runner

import (
	"context"
	"strings"

	"github.com/yi-nology/argus/internal/effect"
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
// 常量单源在 effect.FixBranchPrefix（#64：对账解析按它重建修复 PR 的源分支，
// 编码器↔解析器回环互钉——改值前先跑 effect/cr_resolver 的回环测试）。
const fixBranchPrefix = effect.FixBranchPrefix

// fixBranchName 修复分支名（v7.0 Verified Fix Loop，按 PR 收敛）：argus/fix/<PR号>。
// 同一 PR 多轮审查收敛到同一修复分支（#62：持久化版本化产物，CAS 更新）。
// argus/ 前缀自有命名空间与常见保护分支（main/master/release/*）无交集——
// branch 形态任何失败都不可能触碰保护分支（阶段 2 验收线）。
func fixBranchName(pr *plugin.PRContext) string { return fixBranchPrefix + pr.Number }

// isFixBranch 源分支是否属于修复分支命名空间（防循环护栏判定）。
func isFixBranch(branch string) bool { return strings.HasPrefix(branch, fixBranchPrefix) }
