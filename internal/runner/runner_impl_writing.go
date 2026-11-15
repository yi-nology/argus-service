// runner_impl_writing.go —— Runner 写作面（describe/changelog/CI 归因/diff 预审）的包级实现。

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/writing"
	"github.com/yi-nology/go-git-platform/provider"
)

// runWriting 的包级实现（转发壳见原文件）。
func runWriting(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	diff *plugin.DiffContext, start time.Time) error {

	svc := &writing.Service{LLM: r.LLM}
	in := writing.Input{
		Platform:     pr.Platform,
		Owner:        pr.Owner,
		Repo:         pr.Repo,
		Number:       pr.Number,
		Title:        pr.Title,
		Body:         pr.Body,
		Author:       pr.Author,
		SourceBranch: pr.HeadBranch,
		TargetBranch: pr.BaseBranch,
		DiffText:     diff.Text,
	}
	if m := storage.DecodeJSONMap(task.ArtifactsJSON); m != nil {
		in.ChangeSummary = m[plugin.ArtifactChangeSummary]
	}

	prov, err := r.SDK.Provider(pr.Platform)
	if err != nil {
		return fmt.Errorf("获取平台 provider 失败: %w", err)
	}

	var out *plugin.WorkflowOutput
	var commentID int64
	switch task.Kind {
	case domain.TaskKindDescribe:
		out, err = r.writeDescribe(ctx, task, pr, svc, in, prov)
		if err != nil {
			return err
		}
	case domain.TaskKindChangelog:
		out, commentID, err = r.writeChangelog(ctx, task, pr, svc, in, prov)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知写作任务 kind: %s", task.Kind)
	}

	// 报告正文：任务头 + 写作产物（与审查报告同 header 格式，TaskAPI 可查）。
	rep := reportview.Report{
		TaskID: task.ID, HeadSHA: pr.HeadSHA, Version: r.Version,
		ShowCompliance: false,
	}
	body := reportview.Render(rep)
	body += "\n## ✍️ 写作产物（" + task.Kind + "）\n\n" + strings.TrimSpace(out.Summary)

	resultJSON, _ := json.Marshal(map[string]any{"kind": task.Kind, "output": out.Summary})
	// start marker 原在 fenced 早退之后、公告之前：走 AfterWriteLive（仅未 fenced）。
	updateMarker := func() {
		r.setStartMarker(ctx, task, fmt.Sprintf(
			"✅ Argus 写作完成（%s）——产物见%s。\n\n<!-- argus:start task_id=%s -->",
			task.Kind, writingTargetLabel(task.Kind, commentID), task.ID))
	}
	fenced, ferr := finishTask(r, ctx, terminalWrite{
		Task: task, Cause: causeDone, Verdict: out.Verdict, Report: body,
		ResultJSON: string(resultJSON), Tokens: out.Tokens, SummaryCommentID: commentID,
		Out: out, Announce: true, AfterWriteLive: updateMarker,
	})
	if ferr != nil {
		return fmt.Errorf("写作结果落库失败: %w", ferr)
	}
	if fenced {
		return nil
	}
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// writeDescribe 的包级实现（转发壳见原文件）。
func writeDescribe(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*plugin.WorkflowOutput, error) {

	d, err := svc.Describe(ctx, in)
	if err != nil {
		return nil, err
	}
	section := writing.RenderDescription(d, task.ID)
	newDesc := writing.MergeDescription(pr.Body, section)
	if _, err := prov.UpdateCR(ctx, pr.Owner, pr.Repo, pr.Number,
		provider.UpdateCROptions{Description: newDesc}); err != nil {
		return nil, fmt.Errorf("PR 描述写回失败: %w", err)
	}
	r.Log.Info("argus.runner.describe_written", "task", task.ID,
		"pr", fmt.Sprintf("%s/%s#%s", pr.Owner, pr.Repo, pr.Number), "title", d.Title)
	return &plugin.WorkflowOutput{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    "**" + d.Title + "**\n\n" + d.Body,
		Tokens:     svc.LLM.UsedTokens(),
	}, nil
}

// writeChangelog 的包级实现（转发壳见原文件）。
func writeChangelog(r *Runner, ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*plugin.WorkflowOutput, int64, error) {

	entry, err := svc.Changelog(ctx, in)
	if err != nil {
		return nil, 0, err
	}
	body := fmt.Sprintf("## 📝 Changelog 条目\n\n%s\n\n<!-- argus:changelog task_id=%s -->",
		entry, task.ID)
	idStr, err := prov.CreateDiscussion(ctx, pr.Owner, pr.Repo, pr.Number,
		provider.DiscussionOptions{Body: body})
	var commentID int64
	if err != nil {
		r.Log.Warn("argus.runner.changelog_comment_failed", "task", task.ID, "error", err.Error())
	} else {
		commentID = parseCommentID(idStr)
	}
	return &plugin.WorkflowOutput{
		Verdict:    "comment",
		PluginsRun: []string{"writing"},
		Summary:    entry,
		Tokens:     svc.LLM.UsedTokens(),
	}, commentID, nil
}

// runCIAnalysis 的包级实现（转发壳见原文件）。
func runCIAnalysis(r *Runner, ctx context.Context, task *storage.Task, start time.Time) error {
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
	// start marker 原在 fenced 早退之后、公告之前：走 AfterWriteLive（仅未 fenced）。
	updateMarker := func() {
		r.setStartMarker(ctx, task, fmt.Sprintf(
			"✅ Argus CI 归因完成——结论见%s。\n\n<!-- argus:start task_id=%s -->",
			writingTargetLabel(task.Kind, commentID), task.ID))
	}
	fenced, ferr := finishTask(r, ctx, terminalWrite{
		Task: task, Cause: causeDone, Verdict: out.Verdict, Report: body,
		ResultJSON: string(resultJSON), Tokens: out.Tokens, SummaryCommentID: commentID,
		Out: out, Announce: true, AfterWriteLive: updateMarker,
	})
	if ferr != nil {
		return fmt.Errorf("归因结果落库失败: %w", ferr)
	}
	if fenced {
		return nil
	}
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}

// runDiffPreview 的包级实现（转发壳见原文件）。
func runDiffPreview(r *Runner, ctx context.Context, task *storage.Task, start time.Time) error {
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
	fenced, ferr := finishTask(r, ctx, terminalWrite{
		Task: task, Cause: causeDone, Verdict: out.Verdict, Report: report,
		ResultJSON: string(resultJSON), Tokens: out.Tokens,
		Out: out, Announce: true,
	})
	if ferr != nil {
		return fmt.Errorf("预审结果落库失败: %w", ferr)
	}
	if fenced {
		return nil
	}
	r.Log.Info("argus.runner.task_done", "task", task.ID, "status", storage.StatusDone,
		"verdict", out.Verdict, "kind", task.Kind,
		"duration_ms", time.Since(start).Milliseconds(), "tokens", out.Tokens)
	return nil
}
