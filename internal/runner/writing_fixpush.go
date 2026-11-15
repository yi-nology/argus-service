// writing_fixpush.go —— 验证通过修复的推送/开 PR/通知与 git 辅助（自 writing.go 拆出）。

package runner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/runtime/procx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/fixverifier"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// pushVerifiedFixes 把验证通过的修复 commit 并按 FixPushMode 分派推送：
//   - "branch"（v7.0 缺省形态）：推 argus/fix/<taskID> 修复分支，再 CreateCR
//     开 PR（源=修复分支，目标=PR 源分支）——修复经 PR 评审进入原分支，
//     不直推任何既有分支；开 PR 失败时分支保留（审计留痕，可手动补开）。
//   - "direct"（v3.74.1 旧形态，ARGUS_FIX_PUSH=direct 显式选择）：直推 PR
//     源分支。
//
// 多条修复合并为一个 commit（同一工作副本同一轮，拆分只会放大冲突面）；
// commit message 带任务 ID 与条数可追溯。branch 形态要求 PR 源分支非空
// （CreateCR 的目标分支），与 direct 的既有守卫同点。
func (r *Runner) pushVerifiedFixes(ctx context.Context, task *storage.Task,
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
	target := pr.HeadBranch
	if mode == "branch" {
		target = fixBranchName(pr)
	}
	// 只 add Apply 实际写入的文件——包装命令产生的未跟踪产物绝不进 commit。
	addArgs := append([]string{"-C", dir, "add", "--"}, slices.Sorted(maps.Keys(written))...)
	msg := fmt.Sprintf("fix(argus): 应用 %d 条沙箱验证通过的审查修复\n\nTask: %s\nPR: %s/%s#%s\n\n"+
		"由 argus 修复验证环自动生成（ARGUS_FIX_PUSH=%s）；每条修改均已在隔离沙箱通过编译验证。如需回退请 revert 本提交。",
		verified, task.ID, pr.Owner, pr.Repo, pr.Number, mode)
	// 写凭证注入（Phase 3）：push 走 WriteToken||Token 的 extraheader（进程级），
	// 修复裸 push 依赖环境 git 凭证的缺口（spec §4 附带核查实锤）。
	pushArgs := append([]string{"-C", dir}, r.authGitArgs(pr.Platform)...)
	pushArgs = append(pushArgs, "push", "origin", "HEAD:"+target)
	gitArgs := [][]string{
		addArgs,
		{"-C", dir, "-c", "user.name=argus-bot", "-c", "user.email=argus@bot.local",
			"commit", "-m", msg},
		pushArgs,
	}
	for _, argv := range gitArgs {
		code, stderr, ok := r.gitRun(ctx, dir, argv)
		if !ok {
			step := argv[2]
			if step == "push" {
				step = "push_failed"
			} else {
				step = step + "_failed"
			}
			r.Log.Warn("argus.runner.fix_push_failed", "task", task.ID, "step", argv[2],
				"code", code, "stderr", logredact.Redact(stderr))
			obs.IncrFixPush(mode, step)
			r.setFixStatus(ctx, task, "push_failed", step)
			// push/commit 失败：清场收尾，不给同 SHA 后续任务留污染树
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "reset", "--hard"})
			_, _, _ = r.gitRun(ctx, dir, []string{"-C", dir, "clean", "-fd"})
			return
		}
	}
	r.Log.Info("argus.runner.fix_pushed", "task", task.ID, "mode", mode, "branch", target,
		"fixes", verified, "files", len(written))
	obs.IncrFixPush(mode, "pushed")
	if mode != "branch" { // branch 形态由 openFixPR 落 pr_created/cr_existed/pr_failed
		r.setFixStatus(ctx, task, "pushed", "")
	}
	// 审计留痕（v3.86.0）：bot 写远端分支是最高敏感动作，必须可追溯
	r.Audit.Log("fix.push", "task", task.ID, "mode", mode, "platform", pr.Platform,
		"owner", pr.Owner, "repo", pr.Repo, "number", pr.Number, "branch", target,
		"fixes", verified, "files", len(written))
	if mode == "branch" {
		r.openFixPR(ctx, task, pr, target, verified)
	}
}

// findOpenFixCR 查同目标的既有 open 修复 PR（按 PR 收敛查重）：源分支精确
// 匹配修复分支、目标=被审 PR 源分支、状态 open。SDK/查询失败返回 nil（视作
// 无既有，宁可多开不吞主链路）。
func (r *Runner) findOpenFixCR(ctx context.Context, pr *plugin.PRContext,
	branch string) *provider.ChangeRequest {
	crs, _, err := r.SDK.ListCRs(ctx, pr.Platform, provider.ListCROptions{
		Owner: pr.Owner, Repo: pr.Repo,
		SourceBranch: branch, TargetBranch: pr.HeadBranch,
		State: provider.CRStateOpened,
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

// notifyFixPRUpdated 收敛回帖（Phase 3）：既有修复 PR 已被本轮 push 刷新——
// 回帖告知评审人修复已更新，不新开 PR。best-effort 同 notifyFixPR。
func (r *Runner) notifyFixPRUpdated(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *provider.ChangeRequest, verified int) {
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

// openFixPR 修复分支开 PR（v7.0 阶段 2）：源=argus/fix/<prNumber>，目标=PR 源
// 分支——合并本 PR 即把修复带进原审查分支，语义与 direct 直推等价但经评审、
// 可回滚（关 PR+删分支）。失败降级：分支已推、审计已留，warn 后可手动补开，
// 绝不阻塞主链路。fork 来源的 PR（源分支不在本仓）CreateCR 会失败，同走降级。
func (r *Runner) openFixPR(ctx context.Context, task *storage.Task,
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
		"- 修复分支 `%s`，每条修改均已在隔离沙箱通过编译验证（%d 条）\n"+
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
		r.Log.Warn("argus.runner.fix_cr_failed", "task", task.ID, "branch", branch,
			"error", err.Error())
		obs.IncrFixPush("branch", "cr_failed")
		r.setFixStatus(ctx, task, "pr_failed", err.Error())
		r.Audit.Log("fix.cr", "task", task.ID, "result", "failed", "platform", pr.Platform,
			"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "error", err.Error())
		return
	}
	obs.IncrFixPush("branch", "cr_created")
	r.setFixStatus(ctx, task, "pr_created", "")
	r.Audit.Log("fix.cr", "task", task.ID, "result", "created", "platform", pr.Platform,
		"owner", pr.Owner, "repo", pr.Repo, "branch", branch, "cr", cr.Number,
		"url", cr.WebURL)
	r.Log.Info("argus.runner.fix_cr_created", "task", task.ID, "branch", branch,
		"cr", cr.Number, "url", cr.WebURL)
	r.notifyFixPR(ctx, task, pr, cr)
}

// notifyFixPR 原 PR 通知评论（v7.0 阶段 2 Review 环入口）：修复 PR 创建成功后
// 在原 PR 回帖带链接——评审人从原审查线一键进入修复 PR。best-effort：评论失败
// 只留痕不重试（修复 PR 本身已在，链接可从审计/分支列表找回），不阻塞主链路。
func (r *Runner) notifyFixPR(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *provider.ChangeRequest) {
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

// gitRun 单条 git 命令执行（2min 超时；ok=false = 非零退出或启动失败）。
// setFixStatus 修复链状态落库（best-effort：状态失败不阻断推送主链路，只留痕）。
func (r *Runner) setFixStatus(ctx context.Context, task *storage.Task, status, detail string) {
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

// authGitArgs 修复链 git 操作的写凭证注入（Phase 3 写凭证分离，同 workcopy
// tools#124 形态）：`-c http.extraheader=Authorization: Basic base64(oauth2:token)`
// 进程级注入——token 不进 remote URL、不落 .git/config；缺省回落读 Token
// （WriteToken 缺省同形）。无平台配置/token 时返回 nil（依赖环境凭证，现状兜底）。
func (r *Runner) authGitArgs(platform string) []string {
	if r.Cfg == nil {
		return nil
	}
	pc, ok := r.Cfg.Platforms[platform]
	if !ok {
		return nil
	}
	token := pc.WriteTokenOf()
	if token == "" {
		return nil
	}
	auth := base64.StdEncoding.EncodeToString([]byte("oauth2:" + token))
	return []string{"-c", "http.extraheader=Authorization: Basic " + auth}
}

func (r *Runner) gitRun(ctx context.Context, dir string, argv []string) (int, string, bool) {
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, stderr, code, err := procx.Run(runCtx, procx.RunRequest{
		Argv: append([]string{"git"}, argv...), Timeout: 2 * time.Minute,
	})
	return code, stderr, err == nil && code == 0
}
