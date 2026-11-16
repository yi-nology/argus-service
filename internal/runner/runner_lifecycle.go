// runner_lifecycle.go —— 单任务生命周期的四个命名子步骤与报告模板裁决
// （v3.167.0 自 runner.go 拆出，顺序语义见 runner.go run() 的四步注释）：
// prepareContext（PR 上下文/stale 判定）→ buildPluginTask（任务组装+注入）→
// recordRegret（学习环记账）→ finalizeAndAnnounce（终态/播报）；budgetGate
// 见 runner_budget.go，handleRunFailure 见 runner_failure.go。

package runner

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// 增量 diff → stale 判定。stale=true 表示 diff 与上次完全一致、stale 终态已
// 落地，调用方直接返回；err 非 nil 为真实失败。
func (r *Runner) prepareContext(ctx context.Context, task *storage.Task, knobs taskKnobs) (
	pr *plugin.PRContext, rc *plugin.RepoConfig, diff *plugin.DiffContext, stale bool, err error) {
	return prepareContext(r, ctx, task, knobs)
}

// resolveReportTemplate 报告模板裁决（v6.10.0）：按仓 yaml review.report_template
// 优先于服务级 ARGUS_REPORT_TEMPLATE 默认；reporttmpl.Resolve 裁决（空/非法值
// 层层回退 standard，配置故障不阻断审查）。
func (r *Runner) resolveReportTemplate(rc *plugin.RepoConfig) string {
	return resolveReportTemplate(r, rc)
}

// resolveReportTemplateChain 完整四级裁决（v6.11.0）：repo_settings admin 层 >
// 仓 yaml > env。任务级（commentcmd --template / API 注入）在 Execute 装配处
// 先于本链判定（任务 Artifacts 已带合法值则本链不执行）。
func (r *Runner) resolveReportTemplateChain(rc *plugin.RepoConfig, knobs taskKnobs) string {
	return resolveReportTemplateChain(r, rc, knobs)
}

// buildPluginTask 任务组装与注入：plugin.Task 骨架 → checkpoint 回流/落库钩子 →
// 仓库指引/附加指令/学习环提示/跨轮一致性 → OCR/SAST/codegraph 前置 →
// 仓库级行为配置进 workflow。
func (r *Runner) buildPluginTask(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	rc *plugin.RepoConfig, diff *plugin.DiffContext, wf *requirement.Workflow) plugin.Task {
	return buildPluginTask(r, ctx, task, pr, rc, diff, wf)
}

// recordRegret 学习环记账：R3.5 反思剔除的条目分别落指纹级误报历史与模式级
// regret（失败只告警，不影响主流程）。
func (r *Runner) recordRegret(ctx context.Context, out *plugin.WorkflowOutput, pr *plugin.PRContext) {
	recordRegret(r, ctx, out, pr)
}

// finalizeAndAnnounce 终态收口：结果落库（fenced）→ debug 留存 → sink 旁路
// 上报 → start marker 收口 → 终态播报与指标。finish 落库失败返回 error 由
// 主失败路径接手（task=failed），fenced 则静默返回（写入已被新执行体接管）。
func (r *Runner) finalizeAndAnnounce(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	out *plugin.WorkflowOutput, res *poster.Result, rep reportview.Report, diff *plugin.DiffContext, start time.Time) error {
	return finalizeAndAnnounce(r, ctx, task, pr, out, res, rep, diff, start)
}
