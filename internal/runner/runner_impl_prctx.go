// runner_impl_prctx.go —— Runner PR 上下文与仓库配置的包级实现。

package runner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

// buildPRContext 的包级实现（转发壳见原文件）。
func buildPRContext(r *Runner, ctx context.Context, task *storage.Task) (*plugin.PRContext, error) {
	cr, err := r.SDK.GetCR(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 PR 详情失败: %w", err)
	}
	repo, err := r.SDK.GetRepo(ctx, task.Platform, task.Owner, task.Repo)
	if err != nil {
		return nil, fmt.Errorf("获取仓库信息失败: %w", err)
	}
	headSHA := cr.HeadSHA
	if headSHA == "" {
		headSHA = task.HeadSHA
	}
	return &plugin.PRContext{
		Platform:      task.Platform,
		Owner:         task.Owner,
		Repo:          task.Repo,
		Number:        task.Number,
		Title:         cr.Title,
		Body:          cr.Description,
		Author:        authorName(cr.Author),
		Labels:        cr.Labels,
		HeadSHA:       headSHA,
		BaseBranch:    cr.TargetBranch,
		HeadBranch:    cr.SourceBranch,
		DefaultBranch: repo.DefaultBranch,
		EventType:     task.EventType,
		CIStatus:      r.fetchCIStatus(ctx, task.Platform, task.Owner, task.Repo, headSHA),
	}, nil
}

// fetchCIStatus 的包级实现（转发壳见原文件）。
func fetchCIStatus(r *Runner, ctx context.Context, platform, owner, repo, sha string) string {
	if sha == "" || r.SDK == nil {
		return ""
	}
	p, err := r.SDK.Provider(platform)
	if err != nil || p == nil {
		return ""
	}
	cs, ok := p.(provider.CommitStatusManager)
	if !ok || !p.Capabilities().CommitStatuses {
		return ""
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	statuses, err := cs.ListCommitStatuses(cctx, owner, repo, sha)
	if err != nil || len(statuses) == 0 {
		return ""
	}
	states := make([]model.CommitStatusState, len(statuses))
	for i, s := range statuses {
		states[i] = s.State
	}
	return string(model.CombineCommitStatusStates(states))
}

// buildDiffIncremental 的包级实现（转发壳见原文件）。
func buildDiffIncremental(r *Runner, ctx context.Context, pr *plugin.PRContext, task *storage.Task,
	totalLimit int, opts ...argusctx.PrepareOptions) (*plugin.DiffContext, error) {
	md, err := r.SDK.GetCRDiff(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		return nil, fmt.Errorf("获取 diff 失败: %w", err)
	}
	// 任一路径只要 Prepare 成功即视为 diffprep 执行（活检）
	defer plugin.ReportLayerExec(ctx, plugin.LayerDiffprep)

	// 非 synchronize 事件 → 全量审查（首次审查 / reopen 等）
	if task.EventType != "cr.synchronize" {
		return argusctx.Prepare(ctx, md, totalLimit, opts...), nil
	}

	// 查询同 PR 最近一次 done 任务的 diff 文本
	lastDiff, err := r.Store.GetLastReviewedDiff(ctx, task.Platform, task.Owner, task.Repo, task.Number)
	if err != nil {
		r.Log.Warn("argus.runner.get_last_diff_failed", "task", task.ID, "error", err.Error())
		return argusctx.Prepare(ctx, md, totalLimit, opts...), nil // 降级全量
	}

	if lastDiff == "" {
		return argusctx.Prepare(ctx, md, totalLimit, opts...), nil // 无前置记录 → 全量
	}

	r.Log.Info("argus.runner.incremental_diff", "task", task.ID,
		"last_diff_chars", len(lastDiff))
	return argusctx.PrepareIncremental(ctx, md, lastDiff, totalLimit, opts...), nil
}

// loadRepoConfig 的包级实现（转发壳见原文件）。
func loadRepoConfig(r *Runner, ctx context.Context, pr *plugin.PRContext) *plugin.RepoConfig {
	branch, err := r.SDK.ResolveBranch(ctx, pr.Platform, pr.Owner, pr.Repo, pr.DefaultBranch)
	if err != nil {
		r.Log.Warn("argus.runner.repo_fetch_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	pr.DefaultBranch = branch
	rc, err := r.SDK.LoadRepoConfig(ctx, pr.Platform, pr.Owner, pr.Repo, branch)
	if err != nil {
		r.Log.Warn("argus.runner.repo_config_failed", "error", err.Error())
		return plugin.EmptyRepoConfig()
	}
	if warns := dispatch.ResolveRepoConfig(rc, r.Dispatcher.Registry()); len(warns) > 0 {
		for _, wn := range warns {
			r.Log.Warn("argus.runner.repo_config_warning", "warning", wn)
		}
	}
	return rc
}

// RepoGuideFor 的包级实现（转发壳见原文件）。
func repoGuideFor(r *Runner, ctx context.Context, platform, owner, repo, headSHA string) string {
	if r.SDK == nil || r.Cfg.RepoGuideOff || headSHA == "" {
		return ""
	}
	key := platform + "/" + owner + "/" + repo + "@" + headSHA
	if cached, _ := repoGuideCache.Get(ctx, key); cached != nil {
		return *cached
	}
	var sections []string
	content := r.repoGuideFile(ctx, platform, owner, repo, headSHA, ".argus/REVIEW.md", repoGuideMaxBytes)
	if content != "" {
		sections = append(sections, content)
	} else {
		r.Log.Debug("argus.runner.repo_guide_absent", "repo", owner+"/"+repo)
	}
	for _, path := range repoGuideAgentFiles {
		agent := r.repoGuideFile(ctx, platform, owner, repo, headSHA, path, repoGuideAgentMaxBytes)
		if agent != "" {
			r.Log.Info("argus.runner.agent_guide_loaded", "repo", owner+"/"+repo, "file", path)
			sections = append(sections, "## Agent 通用规范（来自 "+path+"）\n\n"+agent)
			break
		}
	}
	merged := strings.Join(sections, "\n\n")
	_ = repoGuideCache.Set(ctx, key, &merged)
	return merged
}

// repoGuideFile 的包级实现（转发壳见原文件）。
func repoGuideFile(r *Runner, ctx context.Context, platform, owner, repo, headSHA, path string, maxBytes int) string {
	raw, err := r.SDK.GetFileContent(ctx, platform, owner, repo, path, headSHA)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	if len(raw) > maxBytes {
		// rune 安全截断：字节切分会腰斩多字节 UTF-8 出非法序列进 R3/R4 prompt。
		var trunc string
		trunc, _ = textutil.TruncRunes(raw, maxBytes/3) // rune≈3 字节，与旧字节预算同量级
		raw = trunc + "\n…（截断：" + path + " 超上限）"
	}
	r.Log.Info("argus.runner.repo_guide_loaded", "repo", owner+"/"+repo, "file", path, "bytes", len(raw))
	return plugin.EscapeReminderTags(raw)
}
