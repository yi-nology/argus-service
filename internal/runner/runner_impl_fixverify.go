// runner_impl_fixverify.go —— Runner 修复验证环的包级实现。

package runner

import (
	"context"
	"strings"

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

// combinedVerifyOK #61 组合补丁最终验证 + diff 范围校验：
//  1. 对 Apply 后的组合树整体执行一次 FixVerifyCmd（单条通过 ≠ 组合可构建）；
//  2. 包装命令不得移动 HEAD（验证期自行 commit 会把越界改动藏进新提交）；
//  3. git diff --name-only HEAD（含已暂存 + 未暂存）必须 ⊆ Apply 写入集——
//     包装命令越界改写 tracked 文件说明其副作用超预期，推送面拒绝。注意不能
//     用裸 `git diff`：它只看未暂存改动，包装命令 `git add` 的越界文件会漏检。
//
// 任一失败：指标 + push_failed 状态 + 清场还原到验证前 HEAD，返回 false
// （调用方不得 commit/push）。
func combinedVerifyOK(r *Runner, ctx context.Context, task *storage.Task, dir, mode string, written map[string]bool) bool {
	// 验证前 HEAD：既是「包装命令不得提交」的基准，也是失败清场的落点——
	// 包装命令若移动了 HEAD，普通 reset --hard 会停在被污染的提交上。
	headBefore, headOK := gitOutput(r, ctx, dir, []string{"-C", dir, "rev-parse", "HEAD"})
	headBefore = strings.TrimSpace(headBefore)
	fail := func(reason, detail string) bool {
		r.Log.Warn("argus.runner.fix_combined_verify_failed", "task", task.ID,
			"reason", reason, "detail", detail)
		obs.IncrFixPush(mode, reason)
		r.setFixStatus(ctx, task, "push_failed", reason)
		reset := []string{"-C", dir, "reset", "--hard"}
		if headOK && headBefore != "" {
			reset = append(reset, headBefore)
		}
		_, _, _ = r.gitRun(ctx, dir, reset)
		_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "clean", "-fd"})
		return false
	}
	if !headOK || headBefore == "" {
		return fail("diff_scope_check_failed", "git rev-parse HEAD 失败")
	}
	if ok, out := fixverifier.VerifyCombined(ctx, fixverifier.Config{
		Cmd:     r.Cfg.FixVerifyCmd,
		Timeout: r.Cfg.FixVerifyTimeout,
	}, dir); !ok {
		return fail("combined_verify_failed", out)
	}
	// 包装命令不得在验证期提交：HEAD 前移意味着越界改动可能已被藏进新提交，
	// 下面的 diff HEAD 不再可见——直接拒绝（只读构建/测试是包装命令的契约）。
	headAfter, ok := gitOutput(r, ctx, dir, []string{"-C", dir, "rev-parse", "HEAD"})
	if !ok {
		return fail("diff_scope_check_failed", "git rev-parse HEAD 失败")
	}
	if strings.TrimSpace(headAfter) != headBefore {
		return fail("combined_verify_head_moved",
			"包装命令在验证期移动了 HEAD（禁止 commit）: "+headBefore+" → "+strings.TrimSpace(headAfter))
	}
	// diff 范围校验：只看 tracked 修改（包装命令的未跟踪产物不进 add，已由
	// add 白名单挡住；这里拦的是 tracked 越界改写）。必须对比 HEAD 全量差异，
	// 索引里的越界改动（git add）同样要拦。注意 gitRun 只回 stderr，文件清单
	// 要走带 stdout 的 gitOutput。
	diffOut, ok := gitOutput(r, ctx, dir, []string{"-C", dir, "diff", "--name-only", "HEAD"})
	if !ok {
		return fail("diff_scope_check_failed", "git diff 执行失败")
	}
	for _, name := range strings.Split(strings.TrimSpace(diffOut), "\n") {
		if name == "" {
			continue
		}
		if !written[name] {
			return fail("diff_out_of_scope", "预期外 tracked 文件被修改: "+name)
		}
	}
	return true
}
