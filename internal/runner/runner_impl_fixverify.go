// runner_impl_fixverify.go —— Runner 修复验证环的包级实现。

package runner

import (
	"context"

	"github.com/yi-nology/argus/internal/fixverifier"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// verifyFixes 的包级实现（转发壳见原文件）。
func verifyFixes(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext, out *plugin.WorkflowOutput) {
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
