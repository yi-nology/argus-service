// writing_ci.go —— CI 失败归因与本地 diff 预审任务入口（自 writing.go 拆出）。

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/writing"
)

// runCIAnalysis CI 失败归因任务入口（独立于 run 管线：push 的 commit 无 CR，
// 不走 prepareContext）。链路：拉失败 job 日志（provider 可选能力 CILogManager，
// 未实现的平台任务失败降级）→ LLM 归因 → 评论回 PR（有 MR 时）→ 落库收口。
func (r *Runner) runCIAnalysis(ctx context.Context, task *storage.Task, start time.Time) error {
	prov, err := r.SDK.Provider(task.Platform)
	if err != nil {
		return fmt.Errorf("获取平台 provider 失败: %w", err)
	}
	ci, ok := prov.(provider.CILogManager)
	if !ok {
		return fmt.Errorf("平台 %s 未实现 CI 日志能力（CILogManager），无法归因", task.Platform)
	}
	// 关联 MR 优先按任务 Number；无 MR 的 push pipeline 评论降级为跳过（产物只落库）。
	jobs, err := ci.ListFailedJobLogs(ctx, task.Owner, task.Repo, task.HeadSHA)
	if err != nil {
		return fmt.Errorf("拉取失败 job 日志失败: %w", err)
	}
	ciJobs := make([]writing.CIJob, 0, len(jobs))
	for _, j := range jobs {
		ciJobs = append(ciJobs, writing.CIJob{
			Name: j.Name, Stage: j.Stage, WebURL: j.WebURL, Trace: j.Trace, Truncated: j.Truncated,
		})
	}
	analysis, err := (&writing.Service{LLM: r.LLM}).AnalyzeCIFailure(ctx, writing.CIInput{
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
		Number: task.Number, Commit: task.HeadSHA, Jobs: ciJobs,
	})
	if err != nil {
		return err
	}
	var commentID int64
	if task.Number != "" {
		idStr, cerr := prov.CreateDiscussion(ctx, task.Owner, task.Repo, task.Number,
			provider.DiscussionOptions{Body: writing.RenderCIAnalysis(analysis, task.ID)})
		if cerr != nil {
			r.Log.Warn("argus.runner.ci_analysis_comment_failed", "task", task.ID, "error", cerr.Error())
		} else {
			commentID = parseCommentID(idStr)
		}
	}
	r.Log.Info("argus.runner.ci_analysis_done", "task", task.ID, "jobs", len(ciJobs))

	out := &plugin.WorkflowOutput{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    analysis.Conclusion,
		Tokens:     r.LLM.UsedTokens(),
	}
	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "analysis": analysis})
	body := "## ✍️ CI 失败归因\n\n**结论**：" + analysis.Conclusion + "\n\n**根因**：" + analysis.RootCause
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone,
		out.Verdict, body, string(resultJSON), "", out.Tokens, commentID); ferr != nil {
		return fmt.Errorf("归因结果落库失败: %w", ferr)
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次归因结果丢弃")
		return nil
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"✅ Argus CI 归因完成——结论见%s。\n\n<!-- argus:start task_id=%s -->",
		writingTargetLabel(task.Kind, commentID), task.ID))
	r.announceTerminal(ctx, task, storage.StatusDone, out.Verdict, out, "")
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusDone)
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// runDiffPreview 本地 diff 预审任务（v3.123.0）：run 在 prepareContext 之前
// 分派（同 ci_analysis，无 MR 语义）。输入 artifacts["diff"]，经 writing.
// ReviewDiff 单轮执行体（与 MCP review_diff 同步路径 prompt 单源），报告落库
// 供提交方 get_report 轮询，不回帖不进 reportview 审查模板。
func (r *Runner) runDiffPreview(ctx context.Context, task *storage.Task, start time.Time) error {
	diff := storage.DecodeJSONMap(task.ArtifactsJSON)[plugin.ArtifactDiffInput]
	report, err := (&writing.Service{LLM: r.LLM}).ReviewDiff(ctx, diff)
	if err != nil {
		return err
	}
	out := &plugin.WorkflowOutput{
		Verdict:    "comment", // 预审无合并门语义，只出意见报告
		PluginsRun: []string{"writing"},
		Summary:    report,
		Tokens:     r.LLM.UsedTokens(),
	}
	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "report": report})
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusDone,
		out.Verdict, report, string(resultJSON), "", out.Tokens, 0); ferr != nil {
		return fmt.Errorf("预审结果落库失败: %w", ferr)
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次预审结果丢弃")
		return nil
	}
	r.announceTerminal(ctx, task, storage.StatusDone, out.Verdict, out, "")
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusDone)
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}
