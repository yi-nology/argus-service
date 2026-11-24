// runner_impl_lifecycle.go —— Runner 单任务生命周期子步骤的包级实现。

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/sink"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// prepareContext 的包级实现（转发壳见原文件）。
func prepareContext(r *Runner, ctx context.Context, task *storage.Task, knobs taskKnobs) (
	pr *plugin.PRContext, rc *plugin.RepoConfig, diff *plugin.DiffContext, stale bool, err error) {

	pr, err = r.buildPRContext(ctx, task)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// 仓库主权配置单次加载（v3.15.0）：diff 路径过滤、附加指令、workflow 严重度
	// 阈值与评论上限共用；加载失败降级为空配置（全默认行为）。
	rc = r.loadRepoConfig(ctx, pr)
	diffOpts := argusctx.PrepareOptions{
		Exclude: rc.Review.ExcludePaths,
		Include: rc.Review.IncludePaths,
	}
	diff, err = r.buildDiffIncremental(ctx, pr, task, knobs.maxDiffChars, diffOpts)
	if err != nil {
		return nil, nil, nil, false, err
	}
	// 增量审查：diff 与上次完全一致 → 标记 stale，跳过 LLM
	if diff.NoNewChanges {
		r.Log.Info("argus.runner.no_new_changes", "task", task.ID,
			"pr", fmt.Sprintf("%s/%s/%s#%s", task.Platform, task.Owner, task.Repo, task.Number))
		staleReason := "增量审查：diff 与上次审查完全一致，无新变更"
		if _, ferr := finishTask(r, ctx, terminalWrite{
			Task: task, Cause: causeStale, FailReason: staleReason, Announce: true,
		}); ferr != nil {
			// 写库失败：原路径仍公告 stale 并记指标（fenced 短路已在漏斗内处理）。
			r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
			r.announceTerminal(ctx, task, storage.StatusStale, "", nil, "")
			plugin.ReportTaskTotal(ctx, pr.Platform, task.Kind, storage.StatusStale)
		}
		return pr, rc, diff, true, nil
	}
	if task.HeadSHA == "" && pr.HeadSHA != "" {
		// 失败补留痕（v5.4.3）：head SHA 没回写 ⇒ 同 SHA 去重（last_head_sha）
		// 对该任务失效，重跑场景会重复审查——盲区变可见。
		if err := r.Store.SetTaskHeadSHA(ctx, task.ID, pr.HeadSHA); err != nil {
			r.Log.Warn("argus.runner.head_sha_write_failed", "task", task.ID, "error", err.Error())
		}
	}
	return pr, rc, diff, false, nil
}

// buildPluginTask 的包级实现（转发壳见原文件）。
func buildPluginTask(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	rc *plugin.RepoConfig, diff *plugin.DiffContext, wf *requirement.Workflow, knobs taskKnobs) plugin.Task {

	ptask := plugin.Task{
		ID:        task.ID,
		Kind:      task.Kind,
		PR:        *pr,
		Diff:      diff,
		Artifacts: storage.DecodeJSONMap(task.ArtifactsJSON),
	}
	if ptask.Artifacts == nil {
		ptask.Artifacts = map[string]string{}
	}
	// P3 opt-in（ARGUS_REACT_STREAM）：开启时插件 ReAct 增量交给宿主 SSE；
	// 关闭时 nil = 旧整段语义（回滚开关）。nil Cfg 安全。
	ptask.DeltaSink = r.deltaSinkFor(task)
	// v6.11.0 报告模板裁决在 Execute 装配后统一做（任务级 > repo_settings >
	// 仓 yaml > env 四级链）——此处不再无条件覆盖任务 Artifacts 里的任务级值。
	// G2 断点续审（v3.12.0）：同任务重试时回流已持久化的 R1/R2 阶段产物，
	// 跳过已成功阶段的重复 LLM 调用（跨任务不生效——新任务无 checkpoint 键）。
	if task.StageArtifacts != "" {
		if m := storage.DecodeJSONMap(task.StageArtifacts); len(m) > 0 {
			for k, v := range m {
				ptask.Artifacts[k] = v
			}
			r.Log.Info("argus.runner.checkpoint_reuse", "task", task.ID, "stages", len(m))
		}
	}
	// 阶段产物落库钩子：R1/R2 每完成一步即持久化（失败只告警——checkpoint 缺失
	// 只影响重试成本，不影响正确性）
	wf.CheckpointSink = func(arts map[string]string) {
		if err := r.Store.MergeTaskStageArtifacts(ctx, task.ID, arts); err != nil {
			r.Log.Warn("argus.runner.checkpoint_save_failed", "task", task.ID, "error", err.Error())
		}
	}
	// 仓库级自定义审查指引（.argus/review-guidelines.md，对标 Copilot 的
	// custom review instructions）：只从默认分支读，缺失/超限静默降级。
	if g := r.fetchReviewGuidelines(ctx, pr); g != "" {
		ptask.Artifacts[plugin.ArtifactReviewGuidelines] = g
	}
	// 仓库附加审查指令（v3.15.0 .reviewagents.yaml review.extra_instructions）：
	// 与 guidelines.md 合并注入（无文件指引时单独成段），同一信任边界。
	if extra := rc.Review.ExtraInstructions; strings.TrimSpace(extra) != "" {
		merged := ptask.Artifacts[plugin.ArtifactReviewGuidelines]
		if merged != "" {
			merged += "\n"
		}
		ptask.Artifacts[plugin.ArtifactReviewGuidelines] = merged +
			"\n===【仓库附加审查指令（.reviewagents.yaml review.extra_instructions）】===\n" + extra
	}
	// 学习环：注入本仓库历史高误报模式提示词（无样本/关闭时无此键）。
	if h := r.reviewHints(ctx, pr); h != "" {
		ptask.Artifacts[plugin.ArtifactReviewHints] = h
	}
	// 跨轮一致性（v3.52.0）：同 PR 此前轮次的 open 意见注入 R4，供评审员保持
	// 结论一致（推翻须显式说明理由）——同 head 重审 verdict 翻转（生产实测
	// request_changes↔comment 漂移）的最小缓解。查询失败降级无此键。
	if prev := r.prevFindingsSection(ctx, pr); prev != "" {
		ptask.Artifacts[plugin.ArtifactPrevFindings] = prev
		r.Log.Info("argus.runner.prev_findings_injected", "task", task.ID,
			"pr", fmt.Sprintf("%s/%s/%s#%s", pr.Platform, pr.Owner, pr.Repo, pr.Number),
			"bytes", len(prev))
	}
	// OCR delegate 委托前置（design §2）：确定性文件分诊 + 仓库规则注入 R4。
	r.injectOCRDelegate(ctx, pr, diff, &ptask, knobs)
	// SAST 确定性前置（v3.15.0）：gitleaks/semgrep 工作副本扫描，产出直进 Merger。
	r.injectSAST(ctx, pr, diff, wf)
	// Go 符号索引（v3.15.0 P0-1）：get_symbol/get_references 工具 + 触达符号注入。
	r.injectCodeGraph(ctx, pr, diff, &ptask, wf)
	// 影响面快照（v3.183.0）：分层管线产出快照（复用 codegraph 索引），JSON 即时
	// 落库 + prompt 段注入 R4；报告影响段在 buildReport 时从快照渲染。
	r.injectImpactView(ctx, task.ID, pr, diff, &ptask, wf)
	// 全文检索（v3.84.0）：search_code 受控 grep 工具的工作副本路径注入。
	r.injectCodeSearch(ctx, pr, diff, wf)
	// 跨仓契约快照（v3.90.0）：related 段声明的关联仓契约文件注入 R4。
	r.injectRelatedContracts(ctx, pr, rc, &ptask)

	// 仓库级行为配置进 workflow（v3.15.0）：min_severity 过滤在 finish 内执行；
	// RepoConfig 非 nil 时 workflow 不再重复加载（省一次 API 往返）。
	wf.RepoConfig = rc
	return ptask
}

// recordRegret 的包级实现（转发壳见原文件）。
func recordRegret(r *Runner, ctx context.Context, out *plugin.WorkflowOutput, pr *plugin.PRContext) {
	// regret 跨轮入库：R3.5 反思剔除的指纹记录误报历史（产出 vs 终审的差值即
	// regret），后续轮次由 Merger 读取标注可信度。失败只告警，不影响主流程。
	// RemovedPrints[i] 与 RemovedFindings[i] 按索引一一对应（reflection 组装保证）：
	// 行不存在时按原始身份内容建行，不再产生空内容幽灵行（v3.8.0）。
	if out.Reflect != nil && len(out.Reflect.RemovedPrints) > 0 {
		for i, fp := range out.Reflect.RemovedPrints {
			var finding domain.Finding
			if i < len(out.Reflect.RemovedFindings) {
				finding = out.Reflect.RemovedFindings[i]
			}
			if err := r.Store.RecordPrintRegret(ctx,
				pr.Platform, pr.Owner, pr.Repo, pr.Number, fp, finding); err != nil {
				r.Log.Warn("argus.regret.record_failed", "error", err.Error())
			}
		}
	}
	// 模式级 regret：完整剔除条目按 plugin+comment 入 pattern_signals。
	// 记账键必须与 PatternPosted 一致（PatternKey 由 plugin+原始评语派生），而
	// RemovedFindings 携带的 Comment 可能已带展示标注（merger 标注先于 R3.5）——
	// 按索引配对的 RemovedPrints[i]（入库 FP）查 prints 行取回原文后再记账，
	// 查不到（行被清理等）才退回携带文本（v3.9.0 审查修复：惯犯条目模式归因丢失）。
	// By 取首段与 merger PatternPosted 同口径（v3.9.1：多插件合并条目的 regret
	// 此前落在 "a,b" 键的影子行，真实模式行只缺不涨）。
	if out.Reflect != nil && len(out.Reflect.RemovedFindings) > 0 && r.Learning.Enabled {
		for i, f := range out.Reflect.RemovedFindings {
			if i < len(out.Reflect.RemovedPrints) {
				if ps, err := r.Store.GetPrintState(ctx,
					pr.Platform, pr.Owner, pr.Repo, pr.Number, out.Reflect.RemovedPrints[i]); err != nil {
					r.Log.Warn("argus.regret.print_state_failed", "error", logredact.Redact(err.Error()))
				} else if ps != nil && ps.Comment != "" {
					f.Comment = ps.Comment
				}
			}
			if err := r.Store.PatternRegret(ctx,
				pr.Platform, pr.Owner, pr.Repo, learning.FirstBy(f.By), f, time.Now()); err != nil {
				r.Log.Warn("argus.regret.pattern_failed", "error", err.Error())
			}
		}
	}
}

// finalizeAndAnnounce 的包级实现（转发壳见原文件）。
func finalizeAndAnnounce(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	out *plugin.WorkflowOutput, res *poster.Result, rep reportview.Report, diff *plugin.DiffContext, start time.Time) error {

	resultJSON, _ := json.Marshal(toResultPayload(out, diff.Text))
	cause := causeDone
	if res.Stale {
		cause = causeStale // findings/指纹已入库，不回帖不覆盖（§10.2）
	}
	status := terminalStatus(cause)
	// 写后旁路（base 顺序：debug → sink → marker → 公告）：fenced 时 base 早退，
	// 因此走 AfterWriteLive（仅未 fenced 时在公告前执行）。
	liveSideEffects := func() {
		// 调试产物留存（ARGUS_DEBUG=1）：R1/R2/R3 原始输出随任务可查（GET /tasks/:id 的 debug 字段）
		if r.Cfg.Debug && len(out.Debug) > 0 {
			if b, e := json.Marshal(out.Debug); e == nil {
				// 失败补留痕（v5.4.3）：debug 产物丢失排查失据——用户开着 DEBUG
				// 却查不到原始输出，得知道是写库失败而不是没生成。
				if err := r.Store.SetTaskDebug(ctx, task.ID, string(b)); err != nil {
					r.Log.Warn("argus.runner.debug_write_failed", "task", task.ID, "error", err.Error())
				}
			}
		}
		// 结果上报（ARGUS_SINK_URL 非空启用）：push 旁路给 review-service 承接展示，
		// 异步且失败只告警——绝不阻断/拖慢审查终态路径。argus 独立运行（未配置）时
		// 整段跳过、零开销，不依赖 review-service 的任何存在。
		if r.Sink.Enabled() {
			merged := []domain.Finding{}
			if out.Merged != nil {
				merged = out.Merged.Findings
			}
			r.Sink.Report(ctx, sink.BuildPayload(task.TenantID, task.ID, pr, merged, out.Verdict,
				out.PluginsRun, time.Since(start).Milliseconds(), out.Tokens, time.Now()))
		}
		// start marker 收口（v3.30.1）：成功路径把「审查中」更新为完成态（此前
		// marker 发布后永不更新，PR 上一直挂着「审查中」）。stale=不回帖由新执行体
		// 接管，marker 保持审查中由其收口。
		if status == storage.StatusDone {
			r.setStartMarker(ctx, task, fmt.Sprintf(
				"✅ Argus 审查完成，结论：**%s**（%s）——报告见下方总结评论。\n\n<!-- argus:start task_id=%s -->",
				strings.ToUpper(out.Verdict), out.Rule, task.ID))
		}
	}
	fenced, err := finishTask(r, ctx, terminalWrite{
		Task:             task,
		Cause:            cause,
		Verdict:          out.Verdict,
		Report:           reportview.Render(rep),
		ResultJSON:       string(resultJSON),
		Tokens:           out.Tokens,
		SummaryCommentID: res.SummaryCommentID,
		Out:              out,
		Announce:         true,
		AfterWriteLive:   liveSideEffects,
	})
	if err != nil {
		return err
	}
	if fenced {
		// 此前无守卫：stale 重置后僵尸执行体会把旧报告覆盖到新执行体的结果上
		// （fenced 日志已在漏斗内记录，旁路也已被漏斗跳过）。
		return nil
	}
	// 按仓趋势埋点（v3.73.0）：findings 密度 + 耗时分布，metrics 面按仓聚合。
	if out.Merged != nil {
		bySev := map[string]int{}
		for _, f := range out.Merged.Findings {
			bySev[f.Severity]++
		}
		for sev, n := range bySev {
			plugin.ReportFindingsByRepo(ctx, pr.Platform, pr.Owner+"/"+pr.Repo, sev, n)
		}
	}
	plugin.ReportTaskDurationByRepo(ctx, pr.Platform, pr.Owner+"/"+pr.Repo, task.Kind, time.Since(start).Seconds())
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", status,
		"verdict", out.Verdict, "rule", out.Rule, "findings", findingsCount(out.Merged),
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}
