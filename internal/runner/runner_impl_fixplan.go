// runner_impl_fixplan.go —— 修复推送的计划与闸内复查（#62/#63）：
// 推送前读远端态出 CAS 计划（归属校验+force-with-lease）、不可逆动作前的取消
// 复查（effect Prepare 阶段语义）。执行体（add/commit/push/开 PR）在
// runner_impl_fixpush.go——计划与执行分职责，变化原因各自独立。

package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// errFixAborted #63 闸内取消复查命中：动作未施加（Prepare 阶段放弃，kernel
// Abandon 撤意图——不落「结果未知」），状态由复查点自行落账。
var errFixAborted = errors.New("runner: 不可逆外部动作前复查发现任务已取消")

// fixCancelled #63 闸内取消复查：ctx 取消（停机/超时传导）或任务终止标记。
// 诚实边界：收口的是「检查与动作之间」的窗口；动作进行中的取消仍由副作用闸
// 幂等键/未知态对账兜底，epoch/Intent 绑定是 v7.5 加固线。
func fixCancelled(r *Runner, ctx context.Context, task *storage.Task) bool {
	if ctx.Err() != nil {
		return true
	}
	if r == nil || r.Store == nil {
		return false // 打桩场景无取消面（同 setFixStatus 的 nil-Store 分支）
	}
	return r.checkCancelled(ctx, task) != nil
}

// prepareFixPush 推送闸 Prepare 阶段（#62+#63）：先取消复查（取消后不再获得
// 平台写入许可），branch 形态再读远端态出 CAS 计划。返回值透传给 Apply。
func prepareFixPush(r *Runner, ctx context.Context, task *storage.Task, _ *plugin.PRContext,
	dir, mode, target string) (any, error) {
	if fixCancelled(r, ctx, task) {
		r.setFixStatus(ctx, task, "skipped_stale", "闸内复查：任务已取消/ctx 结束")
		return nil, errFixAborted
	}
	if mode != "branch" {
		return nil, nil
	}
	plan, perr := planFixBranchPush(r, ctx, task, dir, target)
	if perr != nil {
		return nil, perr
	}
	return plan, nil
}

// fixCRCancelled 修复 PR 创建闸的取消复查（#63）：分支已推、PR 未开——落
// pr_skipped（下轮审查按 PR 收敛查重复开，不丢修复）。
func fixCRCancelled(r *Runner, ctx context.Context, task *storage.Task) error {
	if fixCancelled(r, ctx, task) {
		r.setFixStatus(ctx, task, "pr_skipped", "闸内复查：任务已取消/ctx 结束")
		return errFixAborted
	}
	return nil
}

// FixCommitEmail/FixCommitSubjectPrefix 修复提交的身份标记（#62 归属校验单源）：
// commit 步骤注入 + planFixBranchPush tip 归属判定共用——远端修复分支 tip 只有
// 带这两重标记才视为「我方产物」，允许 CAS 重写；否则视为人工提交，拒绝覆盖。
// 导出（#64）：push 对账探针按「subject 前缀 + Task: <id>」双重标记在平台提交
// 检索里识别我方修复提交（平台 CRUser 无邮箱字段，作者以提交消息标记为准）。
const (
	FixCommitEmail         = "argus@bot.local"
	FixCommitSubjectPrefix = "fix(argus):"
)

// fixPushPlan branch 模式推送计划（#62 修复分支连续更新）。
type fixPushPlan struct {
	argv   []string // push 步骤 argv
	cas    bool     // true = --force-with-lease CAS 形态
	remote string   // 远端既有 tip SHA（cas=true 时非空）
}

// planFixBranchPush 制定修复分支推送计划：目标分支不存在 → 普通快进 push；
// 已存在 → fetch 后校验 tip 归属（我方 bot 提交才允许重写），以
// --force-with-lease=<ref>:<expected> CAS 更新——两个执行者并发时只有一个
// 成功（lease 过期方被裸仓拒绝），人工提交在 tip 时直接拒绝（不覆盖）。
// 拒绝/预取失败时落 push_failed 并清场，返回 error（调用方不得继续 push）。
func planFixBranchPush(r *Runner, ctx context.Context, task *storage.Task, dir, target string) (fixPushPlan, error) {
	ref := "refs/heads/" + target
	fail := func(reason, detail string) (fixPushPlan, error) {
		r.Log.Warn("argus.runner.fix_branch_plan_failed", "task", task.ID,
			"branch", target, "reason", reason, "detail", detail)
		obs.IncrFixPush("branch", reason)
		r.setFixStatus(ctx, task, "push_failed", reason)
		_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "reset", "--hard"})
		_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "clean", "-fd"})
		return fixPushPlan{}, fmt.Errorf("修复分支推送计划失败: %s", reason)
	}
	out, ok := gitOutput(r, ctx, dir, []string{"-C", dir, "ls-remote", "origin", ref})
	if !ok {
		return fail("branch_probe_failed", "ls-remote 失败")
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		// 分支不存在：普通快进 push（首轮形态）
		return fixPushPlan{argv: []string{"-C", dir, "push", "origin", "HEAD:" + target}}, nil
	}
	remote := fields[0]
	if code, stderr, ok := r.gitRun(ctx, dir, []string{"-C", dir, "fetch", "-q", "origin", target}); !ok {
		return fail("branch_prefetch_failed", fmt.Sprintf("code=%d stderr=%s", code, stderr))
	}
	email, ok := gitOutput(r, ctx, dir, []string{"-C", dir, "log", "-1", "--format=%ce", remote})
	if !ok {
		return fail("branch_tip_inspect_failed", "无法读取 tip 提交者")
	}
	subject, _ := gitOutput(r, ctx, dir, []string{"-C", dir, "log", "-1", "--format=%s", remote})
	if strings.TrimSpace(email) != FixCommitEmail ||
		!strings.HasPrefix(strings.TrimSpace(subject), FixCommitSubjectPrefix) {
		return fail("branch_head_not_argus",
			"修复分支 tip 非我方提交（人工修改过），拒绝覆盖: "+strings.TrimSpace(subject))
	}
	// tip 是我方上一轮修复提交：受控 CAS 重写（期望旧 SHA），禁止无条件 --force
	return fixPushPlan{
		argv: []string{"-C", dir, "push",
			"--force-with-lease=" + ref + ":" + remote, "origin", "HEAD:" + target},
		cas: true, remote: remote,
	}, nil
}
