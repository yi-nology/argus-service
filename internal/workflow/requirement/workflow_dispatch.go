// workflow_dispatch.go —— R4 专家委派：路由、预算裁剪、fan-out、共识、
// SAST 并入与 G4 受限补审。

package requirement

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
)

// dispatchR4 R4 专家委派：确定性路由 → 预算裁剪 → fan-out → 编队共识 → SAST
// 并入 → G4 受限补审。返回裁决所需的全量产物（sel 供完备性裁判、retryNotes
// 供降级注明）。
func (w *Workflow) dispatchR4(ctx context.Context, task plugin.Task, budget *llm.Budget,
	rc *plugin.RepoConfig, changedFiles, skills []string) (sel []dispatch.Selection,
	outcomes []dispatch.Outcome, pruned, retryNotes []string) {

	stageStart := time.Now()
	w.onStage(plugin.LayerR4)

	// R4：确定性路由 → 同任务重试回流（v3.154.0）→ 预算裁剪 → fan-out
	sel = w.Dispatcher.Select(&task.PR, changedFiles, skills, rc)
	// 逐插件 checkpoint 回流：命中 ckpt_r4/<plugin> 的插件直接复用产物——
	// 进程崩溃/发布打断后的同任务重试不再整批重跑（R4 是 LLM 主体成本）。
	// 复用产物并入 outcomes（视为已执行），执行集只含未命中插件。
	execSel, restoredSel, restored := restoreR4Outcomes(task.Artifacts, sel)
	if len(restored) > 0 {
		w.Log.Info("argus.r4.ckpt_reuse", "restored", len(restored), "total", len(sel))
	}
	execSel, pruned = w.Dispatcher.PruneByBudget(execSel, budget)
	outcomes = append(outcomes, restored...)
	outcomes = append(outcomes, w.Dispatcher.RunWithHook(ctx, task, execSel, budget, w.r4CkptHook)...)
	// sel 返回口径 = 覆盖全集（复用 + 本轮执行保留），G4 完备性裁判据此判覆盖
	sel = append(restoredSel, execSel...)
	// v3.4 编队共识模式：consensus 编队不进 fan-out（Select 已排除），在
	// fan-out 后单独召集——成员黑板互看、共识收敛，产出按成员归属进 Merger。
	for _, sq := range w.Dispatcher.ConsensusSquads(&task.PR, changedFiles) {
		outcomes = append(outcomes, w.Dispatcher.RunConsensus(ctx, task, sq, budget)...)
	}
	// SAST 确定性前置（v3.15.0）：产出并入合并（密钥泄漏等确定性结论可直接驱动
	// R5.2 一票否决）；零 LLM、不计任务 token 预算。
	if len(w.SASTOutcomes) > 0 {
		outcomes = append(w.SASTOutcomes, outcomes...)
	}
	// G4 受限补审（opt-in）：执行失败的插件在剩余预算内重试一次（v3.11.0）
	if w.CompletenessRetry {
		outcomes, retryNotes = w.retryFailedPlugins(ctx, task, budget, sel, outcomes)
	}
	plugin.ReportStageDuration(plugin.LayerR4, time.Since(stageStart))
	plugin.ReportLayerExec(plugin.LayerR4)
	return sel, outcomes, pruned, retryNotes
}

// retryFailedPlugins G4 受限补审：对执行失败（Err 非空且非任务取消跳过）的插件
// 重试一次。约束：剩余预算 >0 才补审；重试经 Dispatcher.Run 照常过预算门/熔断门
// （已熔断插件会被跳过，保留原错误）；重试成功**替换**原 outcome——不叠加 findings，
// 不产生双重指纹。consensus 产物不重试（黑板轮转语义由 RunConsensus 自管）。
func (w *Workflow) retryFailedPlugins(ctx context.Context, task plugin.Task, budget *llm.Budget,
	sel []dispatch.Selection, outcomes []dispatch.Outcome) ([]dispatch.Outcome, []string) {

	selByName := make(map[string]dispatch.Selection, len(sel))
	for _, s := range sel {
		selByName[s.Reg.Manifest.Name] = s
	}
	var retrySel []dispatch.Selection
	for _, o := range outcomes {
		if o.Err != nil && o.SkippedReason == "" {
			if s, ok := selByName[o.Name]; ok {
				retrySel = append(retrySel, s)
			}
		}
	}
	if len(retrySel) == 0 {
		return outcomes, nil
	}
	if budget != nil && budget.Remaining() <= 0 {
		return outcomes, []string{"受限补审跳过：任务 token 预算已耗尽"}
	}
	names := make([]string, 0, len(retrySel))
	for _, s := range retrySel {
		names = append(names, s.Reg.Manifest.Name)
	}
	w.Log.Info("argus.completeness.retry", "plugins", strings.Join(names, ","))
	retried := w.Dispatcher.Run(ctx, task, retrySel, budget)
	retryByName := make(map[string]dispatch.Outcome, len(retried))
	for _, o := range retried {
		retryByName[o.Name] = o
	}
	var notes []string
	for i := range outcomes {
		o := &outcomes[i]
		if o.Err == nil || o.SkippedReason != "" {
			continue
		}
		r, ok := retryByName[o.Name]
		if !ok {
			continue
		}
		switch {
		case r.Err == nil && r.SkippedReason == "":
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试成功（原执行失败）", o.Name))
			o.Result, o.Err, o.Duration, o.Manifest, o.SkippedReason = r.Result, nil, r.Duration, r.Manifest, ""
		case r.SkippedReason != "":
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试被跳过（%s）", o.Name, r.SkippedReason))
		default:
			notes = append(notes, fmt.Sprintf("受限补审：%s 重试仍失败", o.Name))
		}
	}
	return outcomes, notes
}
