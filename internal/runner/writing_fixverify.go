// writing_fixverify.go —— 修复验证环与修复分支命名空间（自 writing.go 拆出）。

package runner

import (
	"context"
	"strings"

	"github.com/yi-nology/argus/internal/fixverifier"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// verifyFixes 修复验证环（v3.74.0）：fixverifier 对带 suggested_fix 的意见做
// 沙箱应用+编译验证并回写 f.Verified。未配置包装命令 / 无工作副本 / 全失败
// 均静默跳过——功能是加分项，永不阻断审查主链路。
func (r *Runner) verifyFixes(ctx context.Context, task *storage.Task, pr *plugin.PRContext, out *plugin.WorkflowOutput) {
	if r.Cfg == nil || r.Cfg.FixVerifyCmd == "" || out.Merged == nil || len(out.Merged.Findings) == 0 || r.WC == nil {
		return
	}
	key := plugin.PRToKey(*pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		r.Log.Debug("argus.runner.fix_verify_skip", "task", task.ID, "reason", err.Error())
		return
	}
	defer r.WC.Release(key)
	results := fixverifier.Verify(ctx, fixverifier.Config{
		Cmd:     r.Cfg.FixVerifyCmd,
		Timeout: r.Cfg.FixVerifyTimeout,
	}, dir, out.Merged.Findings)
	verified := 0
	for i := range results {
		if results[i].Skipped == "" {
			obs.IncrFixVerify(results[i].OK) // 阶段 3：有效率分母（仅真实验证尝试）
		}
		if results[i].OK {
			out.Merged.Findings[i].Verified = true
			verified++
		}
	}
	if verified > 0 {
		r.Log.Info("argus.runner.fix_verified", "task", task.ID,
			"verified", verified, "total", len(results))
		// push 环（opt-in，形态由 FixPushMode 分派）：验证通过的修复自动 commit
		// 并推送。任一步失败静默降级为不 push——下轮审查在新的 head 上重新验证
		// 再战，绝不阻塞主链路。
		// 防循环护栏（v7.0 阶段 2）：被审 PR 的源分支是 argus/fix/* = 本环自己
		// 产出的修复 PR——再进 push 环会无限开修复 PR。修复 PR 只评审不合流，
		// 验证标注照常（渲染面），push/开 PR 跳过。
		if r.Cfg.FixPush && !isFixBranch(pr.HeadBranch) {
			r.pushVerifiedFixes(ctx, task, pr, dir, out.Merged.Findings, verified)
		}
	}
}

// fixBranchPrefix 修复分支命名空间（fixBranchName 与防循环护栏共用单源）。
const fixBranchPrefix = "argus/fix/"

// fixBranchName 修复分支名（v7.0 Verified Fix Loop）：argus/fix/<taskID>。
// argus/ 前缀自有命名空间与常见保护分支（main/master/release/*）无交集——
// branch 形态任何失败都不可能触碰保护分支（阶段 2 验收线）。
func fixBranchName(pr *plugin.PRContext) string { return fixBranchPrefix + pr.Number }

// isFixBranch 源分支是否属于修复分支命名空间（防循环护栏判定）。
func isFixBranch(branch string) bool { return strings.HasPrefix(branch, fixBranchPrefix) }
