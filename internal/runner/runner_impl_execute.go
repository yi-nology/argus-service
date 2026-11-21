// runner_impl_execute.go —— Runner 任务执行主面与失败收口的包级实现。

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/gakloop"
	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/reporttmpl"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/toolpool"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// Execute 的包级实现（转发壳见原文件）。
func execute(r *Runner, ctx context.Context, taskID string) error {
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	// 租户注入（#84/#88）：任务执行全程（记忆索引/知识检索/副作用/计费）按
	// 任务归属租户走存储谓词与命名空间——ctx 是唯一的贯穿载体。
	ctx = tenant.NewContext(ctx, task.TenantID)
	// 按仓旋钮单次加载（v3.187.0）：repo_settings 覆盖 → 全局默认，预算闸/
	// 任务预算/diff 上限三处消费全程共用（runner_knobs.go）。
	knobs := r.loadTaskKnobs(ctx, task)
	taskCost := llm.NewCostTracker()
	// 任务级 MCP 调用元数据：随 ctx 流入该任务全部工具调用的 _meta，server 侧
	// 可关联调用来源（哪次评审在调我）；Q&A 等非任务路径无注入。
	ctx = toolpool.WithCallMeta(ctx, map[string]any{
		"task_id": taskID,
		"repo":    task.Owner + "/" + task.Repo,
		"mr":      task.Number,
	})
	// T12 steer 吸收：任务执行全程挂协作中止信号（控制面终止时 fireSteer）。
	// 下游可用 gakloop.IsSteered 区分「被终止/接管」与「用户取消/进程停机」。
	ctx, steerFire := gakloop.Signal(ctx)
	ctx = withSteerFire(ctx, steerFire)

	// Budget 闸后创建（Phase1①）：可能仍为 nil（panic 发生在闸前），失败收尾
	// 路径必须 nil 守卫。
	var budget *llm.Budget
	// v3.185.4：任务体统一 panic 出口——上游 queue/worker 的 panic 隔离只记
	// 日志不留终态（心跳过期复位重跑），对确定性 panic 是「复位→再炸」死循环
	// （live 实测：影响面注入 nil map panic 后任务卡 running）；argus 的
	// failed-retry 退避是重试正道，panic 必须经 handleRunFailure 落 failed 终态。
	if err = guardPanic(r.Log, task.ID, func() error {
		// #48 操作员终止：首个检查点放在任何副作用之前（start marker 也会写平台）。
		if cerr := r.checkCancelled(ctx, task); cerr != nil {
			return cerr
		}
		start := time.Now()
		// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
		r.postStartMarker(ctx, task)

		// Phase1①：先闸后建——intercepted 时 Budget 尚未创建（也不需要）。
		// 租户级预算（#93）：profile 的 real_token_budget 覆盖优先——租户
		// 超限只熔断本租户新任务，不再全局株连；0/未配置继承全局标量。
		realBudget := r.Cfg.RealTokenBudget
		if rb := r.Cfg.ProfileFor(task.TenantID).RealTokenBudget; rb > 0 {
			realBudget = rb
		}
		intercepted, granted := r.budgetGate(ctx, task, knobs, realBudget)
		if intercepted {
			return nil
		}
		budget = dispatch.NewTokenBudget(int(granted))
		ctx = r.execContext(ctx, task, budget, taskCost)
		wf := r.wireWorkflow(ctx, task, budget)
		r.publish(progress.EventTaskRunning, task, "", "")

		if rerr := r.run(ctx, task, wf, budget, start, taskCost, knobs); rerr != nil {
			r.handleRunFailure(ctx, task, budget, rerr)
		}
		return nil
	}); err != nil {
		// 守卫唯一上抛 = 任务体 panic：此时 run 已中断，handleRunFailure 未及
		// 执行——在守卫外补一次失败终态（其自身若再炸仅日志，终态缺失由心跳
		// 复位兜底，但确定性 panic 已从「无限复位重炸」变为 failed-retry 退避）
		defer func() {
			if rec := recover(); rec != nil {
				r.Log.Error("argus.runner.finalize_panic", "task", task.ID,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			}
		}()
		r.handleRunFailure(ctx, task, budget, err)
		return err
	}
	return nil
}

// run 的包级实现（转发壳见原文件）。
func run(r *Runner, ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *llm.Budget, start time.Time, taskCost *llm.CostTracker, knobs taskKnobs) error {

	// #48 操作员终止检查点①：任何平台写/LLM 调用之前（CI 归因与 diff 预审也在
	// 其后分派，取消后不再开始新的工作）。
	if err := r.checkCancelled(ctx, task); err != nil {
		return err
	}

	// CI 失败归因（v3.71.0）：不依赖 MR 语义（push 的 commit 无 CR 可拉），
	// 在 prepareContext 之前分派——输入是失败 job 日志而非 diff。
	if task.Kind == domain.TaskKindCIAnalysis {
		return r.runCIAnalysis(ctx, task, start)
	}

	// 本地 diff 预审（v3.123.0）：同 ci_analysis 不依赖 MR 语义，prepareContext
	// 之前分派——输入是提交方送来的 diff 文本（artifacts），无平台拉取。
	if task.Kind == domain.TaskKindDiffPreview {
		return r.runDiffPreview(ctx, task, start)
	}

	// #48 检查点②：prepareContext 之前（平台拉取与 stale 终态写入之前）。
	if err := r.checkCancelled(ctx, task); err != nil {
		return err
	}

	pr, rc, diff, stale, err := r.prepareContext(ctx, task, knobs)
	if err != nil {
		return err
	}
	if stale {
		return nil // stale 终态已落地
	}

	// 写作面分支（v3.71.0）：describe/changelog 不进 R 系列审查管线与插件派发，
	// 单步 LLM 写作 → 平台写回，独立收口。
	if task.Kind == domain.TaskKindDescribe || task.Kind == domain.TaskKindChangelog {
		return r.runWriting(ctx, task, pr, diff, start)
	}

	ptask := r.buildPluginTask(ctx, task, pr, rc, diff, wf)
	r.applyTaskReportTemplate(&ptask, task, rc, knobs)

	// Phase 2 Evidence v2：证据核验读取面注入（任务期②字段；wf 跨任务复用，
	// Run 返回必清防 stale 泄漏）。pr 非 nil（CI/diff 分支已早退）且有池才注入；
	// 构造失败（无凭证等）保持 nil=集合级降级，不阻断审查。
	if pr != nil && r.WC != nil {
		wf.SourceReader = newWorkcopySourceReader(ctx, r.wcFor(ctx), worktreeKey(pr))
		defer func() { wf.SourceReader = nil }()
	}

	// #48 检查点③：进入 LLM 工作流之前（R 系列是耗时最长、token 消耗最大的段）。
	if err := r.checkCancelled(ctx, task); err != nil {
		return err
	}

	var out *plugin.WorkflowOutput
	switch task.Kind {
	case domain.TaskKindRequirementCompliance:
		out, err = wf.Run(ctx, ptask, budget)
	default: // code_review / visual_check：R4+R5 only
		out, err = wf.RunReviewOnly(ctx, ptask, budget)
	}
	if err != nil {
		return err
	}

	r.recordRegret(ctx, out, pr)

	// #48 检查点④：修复验证环/回帖之前（两者都会产生平台写：沙箱推送与评论）。
	// 取消后不再发评论；修复推送侧另有 cur.Status != running → skipped_stale 守卫
	// 与闸内取消复查（#63 prepareFixPush）。
	if err := r.checkCancelled(ctx, task); err != nil {
		return err
	}

	// 组装报告（规模统计/CI 状态/影响面段/行内上限，详见 helper 注释）。
	rep := r.assembleReport(task, pr, ptask, out, taskCost, rc)

	// 修复验证环（v3.74.0）：配置了隔离包装命令且本次审查有工作副本可用时，
	// 对带 suggested_fix 的意见做沙箱应用+编译验证，通过者渲染「✅ 沙箱验证
	// 通过」。任何失败都静默降级为不标注——验证环绝不阻断审查主链路。
	r.verifyFixes(ctx, task, pr, out)

	// #63 回帖前复查：④与回帖之间隔着报告组装+修复验证环（可含沙箱构建与
	// Git push，分钟级窗口）——不可逆评论发出前再收一次口。
	if err := r.checkCancelled(ctx, task); err != nil {
		return err
	}

	// 回帖（staleness 门控在 poster 内）。
	res, err := r.Poster.Post(ctx, task, *pr, diff, out.Verdict, rep)
	// 活检：回帖层是否被真正调用（成功/失败/stale 都算执行过）
	plugin.ReportLayerExec(ctx, plugin.LayerPoster)
	if err != nil {
		// 回帖最终失败 → task=failed + {plugin=poster} 计数（§10.2）
		plugin.ReportPluginFailure(ctx, "poster")
		return fmt.Errorf("回帖失败: %w", err)
	}

	return r.finalizeAndAnnounce(ctx, task, pr, out, res, rep, diff, start)
}

// handleRunFailure 的包级实现（转发壳见原文件）。
func handleRunFailure(r *Runner, ctx context.Context, task *storage.Task, budget *llm.Budget, runErr error) {
	// #48 操作员终止：控制面已把任务置 failed（且 next_retry_at=NULL）。runner
	// 不得再写终态（会被 status=running fencing 拒绝，但仍可能扰动公告/指标）、
	// 不公告、不安排重试——只留一行 info 取证。
	if errors.Is(runErr, ErrTaskCancelled) {
		r.Log.Info("argus.runner.task_cancelled_by_operator", "task", task.ID,
			"cause", classifyRunEnd(ctx, runErr),
			"action", "控制面已置 failed，跳过终态写入/公告/重试")
		return
	}
	if ctx.Err() != nil {
		r.Log.Warn("argus.runner.task_cancelled", "task", task.ID,
			"cause", classifyRunEnd(ctx, runErr),
			"action", "保持 running，待心跳过期重置回 pending")
		return
	}
	errMsg := logredact.Redact(runErr.Error())
	r.Log.Error("argus.runner.task_failed", "task", task.ID,
		"cause", classifyRunEnd(ctx, runErr), "error", errMsg)
	used := 0
	if budget != nil { // Phase1①：panic 可能发生在 Budget 创建前
		used = budget.Used()
	}
	// PR 侧失败告知（best-effort，平台不可达时静默放弃）：failed-retry 扫
	// 5m×3^n 退避后重排。原顺序是「写库 → marker → 公告」——marker 经 AfterWrite
	// 在 fenced 判定前执行（fenced 时也照旧运行），写库失败时错误分支补跑。
	updateMarker := func() {
		if cur, gerr := r.Store.GetTask(ctx, task.ID); gerr == nil {
			var body string
			switch {
			case r.Cfg.TaskMaxRetries > 0 && cur.Attempt < r.Cfg.TaskMaxRetries:
				body = fmt.Sprintf("⚠️ Argus 审查中断（第 %d 次尝试失败），数分钟后将自动重试。\n> %s\n\n<!-- argus:start task_id=%s -->",
					cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
			default:
				body = fmt.Sprintf("❌ Argus 审查失败（已尝试 %d 次）：\n> %s\n\npush 新提交可重新触发审查。\n\n<!-- argus:start task_id=%s -->",
					cur.Attempt, textutil.TruncEllipsis(errMsg, 300), task.ID)
			}
			r.setStartMarker(ctx, task, body)
		} else {
			r.Log.Warn("argus.runner.fail_notify_skip", "task", task.ID, "error", gerr.Error())
		}
	}
	_, ferr := finishTask(r, ctx, terminalWrite{
		Task: task, Cause: causeFailed, FailReason: errMsg, Tokens: used,
		Announce: true, AfterWrite: updateMarker,
	})
	if ferr != nil {
		// 写库失败：AfterWrite 未执行，按原失败路径补跑 marker 后公告并记指标。
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
		updateMarker()
		r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, errMsg)
		plugin.ReportTaskTotal(ctx, task.Platform, task.Kind, storage.StatusFailed)
	}
}

// applyTaskReportTemplate 的包级实现（转发壳见原文件）。
func applyTaskReportTemplate(r *Runner, ptask *plugin.Task, task *storage.Task,
	rc *plugin.RepoConfig, knobs taskKnobs) {
	if v := ptask.Artifacts[plugin.ArtifactReportTemplate]; !reporttmpl.IsValid(v) {
		if v != "" {
			r.Log.Warn("argus.runner.task_template_invalid", "task", task.ID, "value", v)
		}
		ptask.Artifacts[plugin.ArtifactReportTemplate] = r.resolveReportTemplateChain(rc, knobs)
	}
}

// assembleReport 的包级实现（转发壳见原文件）。
func assembleReport(r *Runner, task *storage.Task, pr *plugin.PRContext, ptask plugin.Task,
	out *plugin.WorkflowOutput, taskCost *llm.CostTracker, rc *plugin.RepoConfig) reportview.Report {
	rep := r.buildReport(task, pr, out, taskCost)
	rep.VerdictText = reportview.VerdictText(out.Rule, countMissingPartial(out.Report))
	rep.Rule = out.Rule
	// v6.11.0 规模统计（analyst 报告头）+ CI 状态（meta 行）：数据就地汇总，
	// 零额外调用（CIStatus 已在 buildPRContext 经 capability 门取好）。
	if ptask.Diff != nil {
		rep.ScaleFiles = len(ptask.Diff.Files)
		for _, f := range ptask.Diff.Files {
			rep.ScaleAdditions += f.Additions
			rep.ScaleDeletions += f.Deletions
		}
	}
	rep.CIStatus = pr.CIStatus
	// 影响面段（v3.183.0）：injectImpactView 产出的快照渲染为评论折叠段
	//（快照已在 ptask.Artifacts，零额外计算；坏 JSON 静默省段——不阻断回帖）。
	if raw := ptask.Artifacts[plugin.ArtifactImpactView]; raw != "" {
		var snap impactview.Snapshot
		if err := json.Unmarshal([]byte(raw), &snap); err == nil {
			rep.ImpactSection = impactview.RenderReportSection(&snap)
			// 交互图路标（v5.6.0 附链接；v5.6.1 任务绑定 share 令牌——MR 评审人
			// 免 API 令牌直开图谱页，secret 空=鉴权关闭时不带参数）。
			if rep.ImpactSection != "" && r.Cfg.PublicURL != "" && len(snap.Nodes) > 0 {
				link := fmt.Sprintf("%s/tasks/%s/impact", r.Cfg.PublicURL, task.ID)
				if tok := impactview.ImpactShareToken(r.Cfg.APIToken, task.TenantID, task.ID); tok != "" {
					link += "?share=" + tok
				}
				rep.ImpactSection += fmt.Sprintf(
					"\n> 🔎 [交互式影响面图](%s)（自包含单页：缩放/拖拽/搜索/类型过滤/邻域聚焦/导出）\n", link)
			}
		}
	}
	// 追溯链段（批 F）：finish 已把 TraceChain 挂 ptask.Artifacts——此处预渲染
	//（纯报告增强零裁决影响；坏 JSON/缺位静默省段不阻断回帖）。
	if tc := plugin.TraceChainOf(ptask.Artifacts); tc != nil && len(tc.Traces) > 0 {
		var tb strings.Builder
		tb.WriteString("\n<details><summary>需求追溯链（issue→AC→代码→测试）</summary>\n\n")
		if tc.SourceRef != "" {
			tb.WriteString("- 来源锚: " + tc.SourceRef + "\n")
		}
		tb.WriteString("\n| AC | 代码 | 测试 | 断链 |\n|---|---|---|---|\n")
		for _, tr := range tc.Traces {
			tests := tr.TestStatus
			if len(tr.Tests) > 0 {
				tests = strings.Join(tr.Tests, ",") + "（" + tr.TestStatus + "）"
			}
			tb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				tr.ACID, strings.Join(tr.CodeFiles, ","), tests, tr.CodeStatus))
		}
		if len(tc.Broken) > 0 {
			tb.WriteString("\n断链: " + strings.Join(tc.Broken, "、") + "\n")
		}
		tb.WriteString("\n</details>\n")
		rep.TraceSection = tb.String()
	}
	// 仓库级行内评论上限（v3.15.0 review.max_comments；0 = 不限，钳制在 poster）
	rep.MaxInlineComments = rc.Review.MaxComments
	return rep
}
