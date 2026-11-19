// runner_impl_inject.go —— Runner 注入面（图/影响面/OCR/SAST/检索/契约）的包级实现。

package runner

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/rag"
	"github.com/oagentkit/textx/codeintel"
	"github.com/oagentkit/textx/diffx"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/tools/sast"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/ocrdeleg"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/sastscan"
	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// fetchReviewGuidelines 的包级实现（转发壳见原文件）。
func fetchReviewGuidelines(r *Runner, ctx context.Context, pr *plugin.PRContext) string {
	if pr == nil || pr.DefaultBranch == "" {
		return ""
	}
	body, err := r.SDK.GetFileContent(ctx, pr.Platform, pr.Owner, pr.Repo,
		reviewGuidelinesPath, pr.DefaultBranch)
	if err != nil || strings.TrimSpace(body) == "" {
		return ""
	}
	t, _ := textutil.TruncRunes(body, reviewGuidelinesMaxRunes)
	return strings.TrimSpace(t)
}

// injectOCRDelegate 的包级实现（转发壳见原文件）。
func injectOCRDelegate(r *Runner, ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task) {
	if r.Cfg == nil || r.Cfg.OcrBin == "" || r.WC == nil || ptask.Kind != domain.TaskKindCodeReview {
		return
	}
	paths := includedPaths(diff)
	if len(paths) == 0 {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.wcFor(ctx).Ensure(ctx, key)
	if err != nil {
		// workcopy 错误链可能携带带凭证 clone URL（git 回显 origin URL），进日志前
		// 统一脱敏——与 §10.2 任务失败信息同纪律
		r.Log.Warn("argus.ocrdeleg.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.wcFor(ctx).Release(key)
	r.injectOCRDelegateInDir(ctx, dir, pr, paths, ptask)
}

// injectOCRDelegateInDir 的包级实现（转发壳见原文件）。
func injectOCRDelegateInDir(r *Runner, ctx context.Context, dir string, pr *plugin.PRContext,
	paths []string, ptask *plugin.Task) {
	began := time.Now()
	cfg := ocrdeleg.Config{Bin: r.Cfg.OcrBin, Timeout: r.Cfg.OcrTimeout}
	base := pr.BaseBranch
	if base == "" {
		base = pr.DefaultBranch
	}
	// unshallow 凭证与 clone 同源（tools#160/#184，单源 deepenAuth）。
	tri, err := ocrdeleg.Preview(ctx, cfg, dir, base, r.deepenAuth(ctx, pr))
	if err != nil {
		// OCR 在工作树里 shell git，stderr 同样可能回显带凭证 URL——先脱敏再进日志
		r.Log.Warn("argus.ocrdeleg.preview_failed", "error", logredact.Redact(err.Error()))
		return
	}
	// preview 成功而 rule 失败：分诊照常注入，规则缺席只是少一组清单。
	groups, err := ocrdeleg.Rules(ctx, cfg, dir, paths)
	if err != nil {
		r.Log.Warn("argus.ocrdeleg.rules_failed", "error", logredact.Redact(err.Error()))
	}
	if s := ocrdeleg.RenderTriage(tri); s != "" {
		ptask.Artifacts[plugin.ArtifactOCRTriage] = s
	}
	if s := ocrdeleg.RenderRules(groups); s != "" {
		ptask.Artifacts[plugin.ArtifactOCRRules] = s
	}
	// 原始产物留档（人工回溯 OCR 原文；DB 只存合并后 findings）——失败仅告警。
	if r.Cfg.OcrHistoryDir != "" {
		err := ocrdeleg.WriteHistory(r.Cfg.OcrHistoryDir, ocrdeleg.HistoryRecord{
			PR:      worktreeKey(pr).String(),
			Base:    base,
			Preview: tri,
			Rules:   groups,
			Took:    time.Since(began).Round(time.Millisecond).String(),
		})
		if err != nil {
			r.Log.Warn("argus.ocrdeleg.history_failed", "error", logredact.Redact(err.Error()))
		}
	}
}

// injectSAST 的包级实现（转发壳见原文件）。
func injectSAST(r *Runner, ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, wf *requirement.Workflow) {
	if !r.Cfg.SASTEnabled() || r.WC == nil {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.wcFor(ctx).Ensure(ctx, key)
	if err != nil {
		// workcopy 错误链可能携带带凭证 clone URL，进日志前统一脱敏
		r.Log.Warn("argus.sast.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.wcFor(ctx).Release(key)
	base := pr.BaseBranch
	if base == "" {
		base = pr.DefaultBranch
	}
	cfg := sast.Config{
		GitleaksBin:   r.Cfg.SASTGitleaksBin,
		SemgrepBin:    r.Cfg.SASTSemgrepBin,
		SemgrepConfig: r.Cfg.SASTSemgrepConfig,
		Timeout:       r.Cfg.SASTTimeout,
	}
	// semgrep 聚焦扫描（v3.21.0）：只扫 diffprep 纳入审查的变更文件——与实际
	// 审查范围一致，避免全仓扫描的时长与信噪比劣化。
	wf.SASTOutcomes = sastscan.Run(ctx, cfg, dir, base, includedPaths(diff), r.Log)
	if len(wf.SASTOutcomes) > 0 {
		r.Log.Info("argus.sast.done", "tools", len(wf.SASTOutcomes))
	}
}

// injectCodeSearch 的包级实现（转发壳见原文件）。
func injectCodeSearch(r *Runner, ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, wf *requirement.Workflow) {
	if !r.Cfg.CodeSearch || r.WC == nil {
		return
	}
	if len(includedPaths(diff)) == 0 {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.wcFor(ctx).Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.codesearch.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.wcFor(ctx).Release(key)
	wf.CodeSearchDir = dir
}

// injectCodeGraph 的包级实现（转发壳见原文件）。
func injectCodeGraph(r *Runner, ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, wf *requirement.Workflow) {
	if !r.Cfg.Codegraph || r.WC == nil {
		return
	}
	if !hasIncludedGoFiles(diff) {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.wcFor(ctx).Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.codegraph.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.wcFor(ctx).Release(key)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		r.Log.Warn("argus.codegraph.index_failed", "error", logredact.Redact(err.Error()))
	} else {
		wf.CodeGraph = idx
		if s := renderTouchedSymbols(ctx, idx, diffx.HunkLines(diff.Text)); s != "" {
			ptask.Artifacts[plugin.ArtifactCodegraphTouched] = s
		}
		r.Log.Info("argus.codegraph.ready", "indexed", idx.IndexedFiles(), "skipped", idx.SkippedFiles())
	}
	// v5.11.0：消歧符号图——Refresh 照常扫文件抽 chunk/边/建图；失败告警降级
	//（不阻断，与上方 codeintel 互不影响）。v6.1.0：embedder 随 ARGUS_SEMSEARCH
	// opt-in 门注入（assemble.go 同款判定），未开即纯图模式（nil）。
	var gx *semsearch.Index
	if r.Cfg.SemSearch && r.Cfg.EmbeddingBaseURL != "" {
		gx = semsearch.NewIndex(semsearch.NewByteBatchEmbedder(rag.NewOpenAIEmbedder(
			r.Cfg.EmbeddingBaseURL, r.Cfg.EmbeddingAPIKey, r.Cfg.EmbeddingModel, r.Cfg.EmbeddingDim)))
	} else {
		gx = semsearch.NewIndex(nil)
	}
	if gerr := gx.Refresh(ctx, dir); gerr != nil {
		r.Log.Warn("argus.codegraph.graph_failed", "error", logredact.Redact(gerr.Error()),
			"searchable", gx.Searchable())
		return
	}
	wf.Graph = gx
	r.Log.Info("argus.codegraph.graph_ready", "chunks", gx.Size(), "searchable", gx.Searchable())
}

// injectImpactView 的包级实现（转发壳见原文件）。
func injectImpactView(r *Runner, ctx context.Context, taskID string, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, wf *requirement.Workflow) {
	if !r.Cfg.ImpactView || r.WC == nil {
		return
	}
	paths := includedPaths(diff)
	if len(paths) == 0 {
		return
	}
	key := worktreeKey(pr)
	pool := r.wcFor(ctx)
	dir, err := pool.Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.impactview.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer pool.Release(key)
	// deepen 凭证与 clone 同源（tools#160/#184，单源 deepenAuth）：funcdiff 的
	// deepen fetch 是唯一漏注凭证的 git fetch——私有平台浅克隆加深必 "could not
	// read Username"，函数级 diff 每次静默降级。
	deepen := r.deepenAuth(ctx, pr)
	idx := wf.CodeGraph
	if idx == nil && hasIncludedGoFiles(diff) {
		// codegraph 关闭但影响面开启：自建索引供 L2（失败降级 L0/L1/L3）
		if idx2, berr := codeintel.NewIndex(dir, 2000); berr == nil {
			idx = idx2
		} else {
			r.Log.Warn("argus.impactview.index_failed", "error", logredact.Redact(berr.Error()))
		}
	}
	snap := impactview.Build(ctx, impactview.Input{
		Dir:          dir,
		ChangedFiles: paths,
		HunkRanges:   diffx.HunkLines(diff.Text),
		DiffText:     diff.Text,
		Index:        idx,
		Graph:        wf.Graph, // v6.0.0：消歧符号图优先解析 L2 调用方（nil = 走既有 codeintel 词法路径）
		MaxCommits:   r.Cfg.ImpactCoupling,
		BaseRef:      impactBaseRef(pr),
		InsecureTLS:  r.Cfg.Platforms[pr.Platform].SkipTLS,
		Deepen:       deepen,
	})
	if len(snap.Nodes) == 0 {
		return
	}
	if b, merr := json.Marshal(snap); merr == nil {
		ptask.Artifacts[plugin.ArtifactImpactView] = string(b)
		if perr := r.Store.MergeTaskArtifacts(ctx, taskID,
			map[string]string{plugin.ArtifactImpactView: string(b)}); perr != nil {
			r.Log.Warn("argus.impactview.persist_failed", "task", taskID, "error", perr.Error())
		}
	}
	if sec := impactview.RenderPromptSection(snap); sec != "" {
		ptask.Artifacts[plugin.ArtifactImpactSummary] = plugin.DataFence(ctx, "改动影响面（静态推导，数据）", sec)
	}
	r.Log.Info("argus.impactview.ready", "task", taskID, "nodes", len(snap.Nodes),
		"pkgs", len(snap.Packages), "calls", snap.Stats.CallSites,
		"coupling", len(snap.Coupling), "risk", snap.Risk.Level, "took_ms", snap.Stats.TookMS)
}

// injectRelatedContracts 的包级实现（转发壳见原文件）。
func injectRelatedContracts(r *Runner, ctx context.Context, pr *plugin.PRContext,
	rc *plugin.RepoConfig, ptask *plugin.Task) {
	if !r.Cfg.RelatedRepos || r.SDK == nil || rc == nil || len(rc.Related) == 0 {
		return
	}
	var files []relatedFile
	for _, c := range rc.Related {
		owner, repo := c.Repo, ""
		if i := strings.IndexByte(c.Repo, '/'); i > 0 {
			owner, repo = c.Repo[:i], c.Repo[i+1:]
		}
		for _, p := range c.Paths {
			ref := c.Ref
			if ref == "" {
				ref = pr.DefaultBranch // 关联仓未指定 ref 时借用本仓默认分支名（内网同构约定），失败则注明
			}
			content, err := r.SDK.GetFileContent(ctx, pr.Platform, owner, repo, p, ref)
			f := relatedFile{Repo: c.Repo, Ref: ref, Path: p}
			if err != nil {
				f.Err = err.Error()
			} else {
				f.Content, f.Truncated = textutil.TruncRunes(content, maxRelatedFileRunes)
			}
			files = append(files, f)
		}
	}
	if s := renderRelatedSection(ctx, files); s != "" {
		ptask.Artifacts[plugin.ArtifactRelatedContracts] = s
		r.Log.Info("argus.related.ready", "files", len(files))
	}
}
