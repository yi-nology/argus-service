// runner_impl_fixpush.go —— Runner 修复推送链（分支/CR/通知/状态）的包级实现。

package runner

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/oagentkit/guardx/logredact"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/fixverifier"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// pushVerifiedFixes 的包级实现（转发壳见原文件）。
func pushVerifiedFixes(r *Runner, ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, dir string, findings []domain.Finding, verified int) {

	mode := r.Cfg.FixPushMode
	if mode == "" { // FixPush=true 但 mode 空（装配期手工置位）：按旧形态兜底
		mode = "direct"
	}
	// 取消标记检查（Phase 3 spec §4「推送前检查取消标记」）：ctx 取消（服务停机/
	// 任务超时传导）或任务已被复位换主（stale 重跑——ResetRunningToPending 把
	// running 打回 pending 后新执行体接管，旧执行体尾部推送=僵尸写）→ 放弃。
	if ctx.Err() != nil {
		r.setFixStatus(ctx, task, "skipped_stale", "ctx 取消")
		return
	}
	if cur, err := r.Store.GetTask(ctx, task.ID); err == nil &&
		cur.Status != storage.StatusRunning {
		r.setFixStatus(ctx, task, "skipped_stale",
			"任务已非 running（stale 重跑/外部改态），放弃推送")
		return
	}
	r.setFixStatus(ctx, task, "pushing", "")
	// 先清场再应用（v3.75.3）：工作副本可能有前轮残留（Apply 中途失败/push 失败
	// 未回滚）与 Verify 期包装命令产物（go build 二进制/coverage）——git add -A
	// 会把这些一并推到作者分支。reset --hard + clean -fd 把树恢复到 checkout 态，
	// Apply 的修改随后写入；push 失败路径同样收尾清场。
	for _, cleanup := range [][]string{
		{"-C", dir, "reset", "--hard"},
		{"-C", dir, "clean", "-fd"},
	} {
		if code, stderr, ok := r.gitRun(ctx, dir, cleanup); !ok {
			r.Log.Warn("argus.runner.fix_clean_failed", "task", task.ID,
				"step", cleanup[2], "code", code, "stderr", logredact.Redact(stderr))
			obs.IncrFixPush(mode, "clean_failed")
			return
		}
	}
	written, err := fixverifier.Apply(ctx, dir, findings)
	if err != nil {
		r.Log.Warn("argus.runner.fix_apply_failed", "task", task.ID, "error", err.Error())
		obs.IncrFixPush(mode, "apply_failed")
		r.setFixStatus(ctx, task, "push_failed", "apply_failed")
		return
	}
	if pr.HeadBranch == "" || len(written) == 0 {
		return
	}
	// #61 组合补丁最终验证：逐条通过 ≠ 组合可构建。Apply 后对组合树整体跑
	// 一次包装命令 + diff 范围校验，任一失败不 commit/push（含清场还原）。
	if !combinedVerifyOK(r, ctx, task, dir, mode, written) {
		return
	}
	target := pr.HeadBranch
	if mode == "branch" {
		target = fixBranchName(pr)
	}
	// 只 add Apply 实际写入的文件——包装命令产生的未跟踪产物绝不进 commit。
	addArgs := append([]string{"-C", dir, "add", "--"}, slices.Sorted(maps.Keys(written))...)
	msg := fmt.Sprintf("fix(argus): 应用 %d 条沙箱验证通过的审查修复\n\nTask: %s\nPR: %s/%s#%s\n\n"+
		"由 argus 修复验证环自动生成（ARGUS_FIX_PUSH=%s）；每条修改已在隔离沙箱逐条验证，组合补丁已通过整体构建验证。如需回退请 revert 本提交。",
		verified, task.ID, pr.Owner, pr.Repo, pr.Number, mode)
	// 写凭证注入（#66）：push 走 WriteToken||Token 的 extraheader——经
	// GIT_CONFIG_* 环境变量（authGitEnv），不进 argv（ps 可见面清零）。
	authEnv := r.authGitEnv(pr.Platform)
	prefix := []gitStep{
		{step: "add", argv: addArgs},
		{step: "commit", argv: []string{"-C", dir, "-c", "user.name=argus-bot",
			"-c", "user.email=" + FixCommitEmail, "commit", "-m", msg}},
	}
	directPush := gitStep{step: "push",
		argv: []string{"-C", dir, "push", "origin", "HEAD:" + target}, env: authEnv}
	// C1 副作用闸：同一 task+head+mode 的推送至多一次。Apply 内的失败照旧落
	// push_failed 并清场；闸的「禁止重试」错误只记日志（状态已由 Apply 落账），
	// 绝不能在这里再跑一遍 git 命令。#62：branch 形态的远端态读取+归属校验+
	// CAS 计划放 Prepare 阶段（闸内、施加前，失败=未施加任何副作用——拒绝
	// 覆盖人工提交不得把 Key 打成「结果未知」，kernel Abandon 语义）。
	act := pushEffectAction(task.ID, pr.HeadSHA, mode, target, pr, tenant.FromContext(ctx))
	outcome, _, err := r.pushEffectRunner().DoPrepared(ctx, act,
		func(ctx context.Context) (any, error) {
			return prepareFixPush(r, ctx, task, pr, dir, mode, target)
		},
		func(ctx context.Context, prepared any, _ effect.Lease) (effect.Result, error) {
			steps := append([]gitStep{}, prefix...)
			if plan, ok := prepared.(fixPushPlan); ok && mode == "branch" {
				steps = append(steps, gitStep{step: "push", argv: plan.argv, env: authEnv})
			} else {
				steps = append(steps, directPush)
			}
			if err := runFixGitPush(r, ctx, task, dir, steps, mode); err != nil {
				return effect.Result{}, err
			}
			return effect.Result{Payload: []byte(target)}, nil
		})
	if err != nil {
		if effect.RetryBlocked(err) {
			r.Log.Error("argus.runner.fix_push_effect_blocked", "task", task.ID, "mode", mode,
				"error", err.Error(), "hint", "副作用闸拒绝/结果未知：不重复推送（避免重复提交）")
			// 终态：pushing 不是终态，重试被闸挡住时必须显式落 skipped，
			// 否则任务看起来永远停在「正在推送」。
			r.setFixStatus(ctx, task, "push_skipped", "副作用闸拒绝/结果未知: "+err.Error())
			r.Audit.Log("fix.push", "task", task.ID, "mode", mode, "result", "skipped",
				"error", err.Error())
		}
		return
	}
	replayed := outcome == effect.OutcomeReplayed
	r.Log.Info("argus.runner.fix_pushed", "task", task.ID, "mode", mode, "branch", target,
		"fixes", verified, "files", len(written), "replayed", replayed)
	if !replayed {
		obs.IncrFixPush(mode, "pushed")
	}
	if mode != "branch" { // branch 形态由 openFixPR 落 pr_created/cr_existed/pr_failed
		r.setFixStatus(ctx, task, "pushed", "")
	}
	// 审计留痕（v3.86.0）：bot 写远端分支是最高敏感动作，必须可追溯
	r.Audit.Log("fix.push", "task", task.ID, "mode", mode, "platform", pr.Platform,
		"owner", pr.Owner, "repo", pr.Repo, "number", pr.Number, "branch", target,
		"fixes", verified, "files", len(written), "replayed", replayed)
	if mode == "branch" {
		r.openFixPR(ctx, task, pr, target, verified)
	}
}

// gitStep 修复链 git 步骤（#66）：step 是显式失败分类标签（add/commit/push），
// 指标/状态不再从 argv 下标猜（附带凭证的 argv[2] 会取到 "-c"，分类失真）；
// 凭证经 env（git 官方 GIT_CONFIG_* 协议）注入，绝不进 argv（ps 可见面清零）。
type gitStep struct {
	step string   // "add" | "commit" | "push"——失败分类/指标标签
	argv []string // git 参数（不含可执行名；凭证禁止出现在这里）
	env  []string // 附加 KEY=VALUE 环境（authGitEnv 产物；nil = 无附加）
}

// runFixGitPush 执行 add/commit/push 步骤；失败时按显式 step 分类落状态并清场。
func runFixGitPush(r *Runner, ctx context.Context, task *storage.Task, dir string,
	steps []gitStep, mode string) error {
	for _, s := range steps {
		code, stderr, ok := r.gitRunEnv(ctx, dir, s.argv, s.env)
		if !ok {
			failed := s.step + "_failed"
			r.Log.Warn("argus.runner.fix_push_failed", "task", task.ID, "step", s.step,
				"code", code, "stderr", logredact.Redact(stderr))
			obs.IncrFixPush(mode, failed)
			r.setFixStatus(ctx, task, "push_failed", failed)
			// push/commit 失败：清场收尾，不给同 SHA 后续任务留污染树
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "reset", "--hard"})
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "clean", "-fd"})
			return fmt.Errorf("修复推送失败: %s", failed)
		}
	}
	return nil
}

// findOpenFixCR 的包级实现（转发壳见原文件）。
func findOpenFixCR(r *Runner, ctx context.Context, pr *plugin.PRContext,
	branch string) *model.ChangeRequest {
	crs, _, err := r.SDK.ListCRs(ctx, pr.Platform, provider.ListCROptions{
		Owner: pr.Owner, Repo: pr.Repo,
		SourceBranch: branch, TargetBranch: pr.HeadBranch,
		State: model.CRStateOpened,
	})
	if err != nil {
		r.Log.Warn("argus.runner.fix_cr_dedup_query_failed", "task", pr.Number,
			"error", err.Error())
		return nil
	}
	for _, cr := range crs {
		if cr != nil && cr.SourceBranch == branch {
			return cr
		}
	}
	return nil
}

// notifyFixPRUpdated 的包级实现（转发壳见原文件）。
func notifyFixPRUpdated(r *Runner, ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *model.ChangeRequest, verified int) {
	if r.SDK == nil {
		return
	}
	link := cr.WebURL
	if link == "" && cr.Number != "" {
		link = fmt.Sprintf("%s/%s/pull/%s", pr.Owner, pr.Repo, cr.Number)
	}
	body := fmt.Sprintf("🔧 修复已更新至 #%s（%d 条沙箱验证通过，Task `%s`）：\n\n"+
		"%s\n\n同目标 PR 复用本修复 PR（按 PR 收敛，不新开）；合并即进入本分支，"+
		"回滚=关闭该 PR。<!-- argus:fix_cr task_id=%s pr=%s -->",
		cr.Number, verified, task.ID, link, task.ID, pr.Number)
	if _, err := r.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body); err != nil {
		r.Log.Warn("argus.runner.fix_notify_failed", "task", task.ID, "error", err.Error())
		return
	}
}

// openFixPR 的包级实现（转发壳见原文件）。
func openFixPR(r *Runner, ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, branch string, verified int) {
	if r.SDK == nil {
		r.Log.Warn("argus.runner.fix_pr_skipped", "task", task.ID, "reason", "sdk 未装配")
		obs.IncrFixPush("branch", "cr_failed")
		return
	}
	// 按 PR 收敛查重（Phase 3 spec §4）：同目标 PR 已有 open 修复 PR（同源分支）
	// → 不新开，回帖更新（分支已被本轮 push 刷新）——「同一 PR 多次审查不产生
	// 多个待处理修复 PR」验收。查重失败按无既有处理（宁可多开也不吞主链路）。
	if existing := r.findOpenFixCR(ctx, pr, branch); existing != nil {
		r.Log.Info("argus.runner.fix_cr_converged", "task", task.ID, "branch", branch,
			"cr", existing.Number)
		obs.IncrFixPush("branch", "cr_existed")
		r.Audit.Log("fix.cr", "task", task.ID, "result", "existed", "platform", pr.Platform,
			"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "cr", existing.Number)
		r.notifyFixPRUpdated(ctx, task, pr, existing, verified)
		r.setFixStatus(ctx, task, "cr_existed", "")
		return
	}
	title := fmt.Sprintf("fix(argus): %d 条沙箱验证通过的审查修复", verified)
	desc := fmt.Sprintf("由 argus 修复验证环自动生成。\n\n"+
		"- 来源任务: %s（PR %s/%s#%s）\n"+
		"- 修复分支 `%s`，每条修改已在隔离沙箱逐条验证，组合补丁已通过整体构建验证（%d 条）\n"+
		"- 合并本 PR 即把修复带进原审查分支；回滚=关闭本 PR 并删除修复分支\n"+
		"- 审计: fix.push(mode=branch) / fix.cr 留痕可追溯",
		task.ID, pr.Owner, pr.Repo, pr.Number, branch, verified)
	crOpts := provider.CreateCROptions{
		Owner:              pr.Owner,
		Repo:               pr.Repo,
		Title:              title,
		Description:        desc,
		SourceBranch:       branch,
		TargetBranch:       pr.HeadBranch,
		RemoveSourceBranch: true,                           // 合并即清理修复分支（回滚面只剩关 PR）
		Draft:              r.Cfg != nil && r.Cfg.FixDraft, // ggp#160 红利（v0.138）
	}
	// C1 副作用闸：修复 PR 创建至多一次。Replayed 时恢复既有 CR 身份继续通知/
	// 状态回写，不重复开 PR。#63：取消复查放 Prepare（未施加任何副作用即放弃，
	// 不落未知态）。
	act := crEffectAction(task.ID, pr.HeadSHA, branch, pr, tenant.FromContext(ctx))
	outcome, res, err := r.crEffectRunner().DoPrepared(ctx, act,
		func(ctx context.Context) (any, error) {
			if cerr := fixCRCancelled(r, ctx, task); cerr != nil {
				return nil, cerr
			}
			return nil, nil
		},
		func(ctx context.Context, _ any, _ effect.Lease) (effect.Result, error) {
			cr, err := r.SDK.CreateCR(ctx, pr.Platform, crOpts)
			// 平台创建面无 Draft 参数（ggp#160 台账：gitlab/gitea/forgejo/tencentcode）→
			// ErrDraftNotSupported 降级非 Draft 重试——能力诚实：有就用没有回落。
			if err != nil && errors.Is(err, provider.ErrDraftNotSupported) {
				r.Log.Warn("argus.runner.fix_draft_unsupported", "task", task.ID,
					"platform", pr.Platform, "hint", "平台不支持 Draft，降级非 Draft 重试")
				crOpts.Draft = false
				cr, err = r.SDK.CreateCR(ctx, pr.Platform, crOpts)
			}
			if err != nil {
				return effect.Result{}, err
			}
			return encodeCRResult(cr), nil
		})
	if err != nil {
		// #63 闸内取消：状态已由 fixCRCancelled 落 pr_skipped，不得再被下面的
		// pr_failed 分支覆写。
		if errors.Is(err, errFixAborted) {
			return
		}
		if effect.RetryBlocked(err) {
			r.Log.Warn("argus.runner.fix_cr_skipped", "task", task.ID, "branch", branch,
				"error", err.Error(), "hint", "副作用闸拒绝/结果未知：不重复创建修复 PR")
			// 终态：不把「被闸挡住」留在 pushing（会被误读成仍在执行）。
			r.setFixStatus(ctx, task, "pr_skipped", "副作用闸拒绝/结果未知: "+err.Error())
			r.Audit.Log("fix.cr", "task", task.ID, "result", "skipped", "platform", pr.Platform,
				"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "error", err.Error())
			return
		}
		r.Log.Warn("argus.runner.fix_cr_failed", "task", task.ID, "branch", branch,
			"error", err.Error())
		obs.IncrFixPush("branch", "cr_failed")
		r.setFixStatus(ctx, task, "pr_failed", err.Error())
		r.Audit.Log("fix.cr", "task", task.ID, "result", "failed", "platform", pr.Platform,
			"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "error", err.Error())
		return
	}
	cr, derr := decodeCRResult(res)
	if derr != nil {
		r.Log.Warn("argus.runner.fix_cr_result_invalid", "task", task.ID, "error", derr.Error())
		r.setFixStatus(ctx, task, "pr_failed", derr.Error())
		r.Audit.Log("fix.cr", "task", task.ID, "result", "failed", "platform", pr.Platform,
			"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "error", derr.Error())
		return
	}
	replayed := outcome == effect.OutcomeReplayed
	if !replayed {
		obs.IncrFixPush("branch", "cr_created")
	}
	r.setFixStatus(ctx, task, "pr_created", "")
	r.Audit.Log("fix.cr", "task", task.ID, "result", "created", "platform", pr.Platform,
		"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "cr", cr.Number,
		"url", cr.WebURL, "replayed", replayed)
	r.Log.Info("argus.runner.fix_cr_created", "task", task.ID, "branch", branch,
		"cr", cr.Number, "url", cr.WebURL, "replayed", replayed)
	// 重放表示该 Key 的修复 PR 已完成创建——通知评论在首次施加时已发过，
	// 再发会形成重复评论；审计/指标仍保留重放事实。
	if !replayed {
		r.notifyFixPR(ctx, task, pr, cr)
	}
}

// notifyFixPR 的包级实现（转发壳见原文件）。
func notifyFixPR(r *Runner, ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *model.ChangeRequest) {
	if r.SDK == nil {
		return
	}
	link := cr.WebURL
	if link == "" && cr.Number != "" {
		link = fmt.Sprintf("%s/%s/pull/%s", pr.Owner, pr.Repo, cr.Number) // 无 URL 时退化为号链
	}
	body := fmt.Sprintf("🔧 已生成修复 PR #%s：%s\n\n"+
		"每条修改均已在隔离沙箱通过编译验证（Task `%s`）；合并即进入本分支，"+
		"回滚=关闭该 PR。<!-- argus:fix_cr task_id=%s pr=%s -->",
		cr.Number, link, task.ID, task.ID, pr.Number)
	if _, err := r.SDK.CreateIssueComment(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number, body); err != nil {
		r.Log.Warn("argus.runner.fix_notify_failed", "task", task.ID, "error", err.Error())
		r.Audit.Log("fix.cr", "task", task.ID, "result", "notify_failed",
			"platform", pr.Platform, "owner", pr.Owner, "repo", pr.Repo, "error", err.Error())
		return
	}
	r.Audit.Log("fix.cr", "task", task.ID, "result", "notified", "platform", pr.Platform,
		"owner", pr.Owner, "repo", pr.Repo, "number", pr.Number, "cr", cr.Number)
}

// setFixStatus 的包级实现（转发壳见原文件）。
func setFixStatus(r *Runner, ctx context.Context, task *storage.Task, status, detail string) {
	if r.Store == nil { // 打桩场景（openFixPR 单测无 Store）：内存态照回写
		task.FixStatus = status
		return
	}
	if err := r.Store.UpdateTaskFixStatus(ctx, task.ID, status); err != nil {
		r.Log.Warn("argus.runner.fix_status_write_failed", "task", task.ID,
			"status", status, "error", err.Error())
		return
	}
	task.FixStatus = status
	if detail != "" {
		r.Audit.Log("fix.status", "task", task.ID, "status", status, "detail", detail)
	}
}
