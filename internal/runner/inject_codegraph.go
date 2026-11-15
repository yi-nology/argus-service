// inject_codegraph.go —— 代码图/影响面注入与触达符号渲染（自 inject.go 拆出）。

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oagentkit/guardx/logredact"
	"github.com/oagentkit/rag"
	"github.com/oagentkit/textx/diffx"

	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/semsearch"
	"github.com/yi-nology/argus/internal/workflow/requirement"

	"github.com/oagentkit/textx/codeintel"
)

// injectCodeGraph Go 符号索引前置（v3.15.0 P0-1）：变更含 Go 文件时在工作副本上
// 以 go/parser 建索引——Workflow.CodeGraph 供 R3/R4 挂 get_symbol/get_references
// 工具；触达符号（diff hunk 行区间与符号定义的交集）渲染为提示词 section 注入
// Artifacts。v5.11.0：同工作副本目录再建消歧符号图（semsearch 纯图模式，
// Workflow.Graph 供挂 get_trace/get_symbol_context——图查询零 embedding 依赖，
// 无 ARGUS_EMBEDDING_* 部署同样可用）。v6.1.0：ARGUS_SEMSEARCH +
// ARGUS_EMBEDDING_BASE_URL 就绪时给图索引注入 embedder——search_sem 进审查
// 管线随既有 opt-in 门（部署显式开的才付每任务 embedding 成本），未开保持
// 纯图模式。codeintel 索引与图索引互不阻断，各自失败告警降级，不阻断审查。
// 索引纯内存，建完即释放工作副本。
func (r *Runner) injectCodeGraph(ctx context.Context, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, wf *requirement.Workflow) {
	if !r.Cfg.Codegraph || r.WC == nil {
		return
	}
	if !hasIncludedGoFiles(diff) {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.codegraph.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.WC.Release(key)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		r.Log.Warn("argus.codegraph.index_failed", "error", logredact.Redact(err.Error()))
	} else {
		wf.CodeGraph = idx
		if s := renderTouchedSymbols(idx, diffx.HunkLines(diff.Text)); s != "" {
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

// injectImpactView 影响面快照前置（v3.183.0）：分层管线（文件/import 闭包/
// 符号调用方/测试面/历史共变，零 LLM）产出快照——JSON 即时落 artifacts_json
// （任务 running 期 MCP/API 即可消费，失败也不丢），prompt 段包数据区围栏注
// R4，报告段在 buildReport 时从快照渲染。复用 codegraph 索引（关闭时自建）；
// ARGUS_IMPACTVIEW=0 / 无纳入文件 / 无工作副本池时跳过。任何失败告警降级，
// 不阻断审查。
func (r *Runner) injectImpactView(ctx context.Context, taskID string, pr *plugin.PRContext,
	diff *plugin.DiffContext, ptask *plugin.Task, wf *requirement.Workflow) {
	if !r.Cfg.ImpactView || r.WC == nil {
		return
	}
	paths := includedPaths(diff)
	if len(paths) == 0 {
		return
	}
	key := worktreeKey(pr)
	dir, err := r.WC.Ensure(ctx, key)
	if err != nil {
		r.Log.Warn("argus.impactview.workcopy_failed", "error", logredact.Redact(err.Error()))
		return
	}
	defer r.WC.Release(key)
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
		ptask.Artifacts[plugin.ArtifactImpactSummary] = plugin.DataFence("改动影响面（静态推导，数据）", sec)
	}
	r.Log.Info("argus.impactview.ready", "task", taskID, "nodes", len(snap.Nodes),
		"pkgs", len(snap.Packages), "calls", snap.Stats.CallSites,
		"coupling", len(snap.Coupling), "risk", snap.Risk.Level, "took_ms", snap.Stats.TookMS)
}

// impactBaseRef 函数级 diff 的 base 参照：PR base 分支优先，缺省回落默认
// 分支（workcopy 克隆的 origin/* 引用由 gitdeepen 保 merge-base 可达）。
func impactBaseRef(pr *plugin.PRContext) string {
	base := pr.BaseBranch
	if base == "" {
		base = pr.DefaultBranch
	}
	if base == "" {
		return ""
	}
	return "origin/" + base
}

// maxTouchedSymbols 触达符号清单上限（防巨型 PR 的清单吃预算）。
const maxTouchedSymbols = 30

// renderTouchedSymbols 汇总触达符号并渲染提示词 section（含可用性提示与数据区纪律）。
func renderTouchedSymbols(idx *codeintel.Index, ranges map[string][][2]int) string {
	seen := map[string]bool{}
	var lines []string
	for file, rs := range ranges {
		for _, r := range rs {
			for _, s := range idx.TouchedByLines(file, r[0], r[1]) {
				name := s.Name
				if s.Recv != "" {
					name = s.Recv + "." + s.Name
				}
				key := s.File + ":" + name
				if seen[key] {
					continue
				}
				seen[key] = true
				lines = append(lines, fmt.Sprintf("- %s %s（%s:%d）", s.Kind, name, s.File, s.Line))
				if len(lines) >= maxTouchedSymbols {
					lines = append(lines, "…（其余略）")
					return renderTouchedSection(lines)
				}
			}
		}
	}
	return renderTouchedSection(lines)
}

func renderTouchedSection(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "以下 %d 个符号的代码行被本次变更触达（代码图索引，可作为审查入口）：\n", len(lines))
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("可用 get_symbol 查定义与签名、get_references 查调用方/使用点。")
	return plugin.DataFence("变更触达符号（代码图索引）", strings.TrimRight(b.String(), "\n"))
}
