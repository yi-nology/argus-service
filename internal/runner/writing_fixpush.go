// writing_fixpush.go —— 验证通过修复的推送/开 PR/通知与 git 辅助（自 writing.go 拆出）。

package runner

import (
	"context"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/domain"
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
	pushVerifiedFixes(r, ctx, task, pr, dir, findings, verified)
}

// findOpenFixCR 查同目标的既有 open 修复 PR（按 PR 收敛查重）：源分支精确
// 匹配修复分支、目标=被审 PR 源分支、状态 open。SDK/查询失败返回 nil（视作
// 无既有，宁可多开不吞主链路）。
func (r *Runner) findOpenFixCR(ctx context.Context, pr *plugin.PRContext,
	branch string) *provider.ChangeRequest {
	return findOpenFixCR(r, ctx, pr, branch)
}

// notifyFixPRUpdated 收敛回帖（Phase 3）：既有修复 PR 已被本轮 push 刷新——
// 回帖告知评审人修复已更新，不新开 PR。best-effort 同 notifyFixPR。
func (r *Runner) notifyFixPRUpdated(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *provider.ChangeRequest, verified int) {
	notifyFixPRUpdated(r, ctx, task, pr, cr, verified)
}

// openFixPR 修复分支开 PR（v7.0 阶段 2）：源=argus/fix/<prNumber>，目标=PR 源
// 分支——合并本 PR 即把修复带进原审查分支，语义与 direct 直推等价但经评审、
// 可回滚（关 PR+删分支）。失败降级：分支已推、审计已留，warn 后可手动补开，
// 绝不阻塞主链路。fork 来源的 PR（源分支不在本仓）CreateCR 会失败，同走降级。
func (r *Runner) openFixPR(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, branch string, verified int) {
	openFixPR(r, ctx, task, pr, branch, verified)
}

// notifyFixPR 原 PR 通知评论（v7.0 阶段 2 Review 环入口）：修复 PR 创建成功后
// 在原 PR 回帖带链接——评审人从原审查线一键进入修复 PR。best-effort：评论失败
// 只留痕不重试（修复 PR 本身已在，链接可从审计/分支列表找回），不阻塞主链路。
func (r *Runner) notifyFixPR(ctx context.Context, task *storage.Task,
	pr *plugin.PRContext, cr *provider.ChangeRequest) {
	notifyFixPR(r, ctx, task, pr, cr)
}

// gitRun 单条 git 命令执行（2min 超时；ok=false = 非零退出或启动失败）。
// setFixStatus 修复链状态落库（best-effort：状态失败不阻断推送主链路，只留痕）。
func (r *Runner) setFixStatus(ctx context.Context, task *storage.Task, status, detail string) {
	setFixStatus(r, ctx, task, status, detail)
}

// authGitArgs 修复链 git 操作的写凭证注入（Phase 3 写凭证分离，同 workcopy
// tools#124 形态）：`-c http.extraheader=Authorization: Basic base64(oauth2:token)`
// 进程级注入——token 不进 remote URL、不落 .git/config；缺省回落读 Token
// （WriteToken 缺省同形）。无平台配置/token 时返回 nil（依赖环境凭证，现状兜底）。
func (r *Runner) authGitArgs(platform string) []string {
	return authGitArgs(r, platform)
}

func (r *Runner) gitRun(ctx context.Context, dir string, argv []string) (int, string, bool) {
	return gitRun(r, ctx, dir, argv)
}
