package qa

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/tools/workcopy"
	"github.com/yi-nology/argus/internal/codesearch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/go-git-platform/provider"
)

// buildSimilarIssueTool 实现 Engine.similarIssueTool（search_similar_issues 构造）。
func buildSimilarIssueTool(e *Engine, platform, owner, repo string) (tool.BaseTool, error) {
	sm, ok := searchManager(e.SDK, platform)
	if !ok {
		return nil, nil
	}
	si, terr := toolutils.InferTool("search_similar_issues",
		"在本仓库内搜索与某个问题/报错相似的历史 issue（判断问题是否已知/重复）。参数 keywords 为检索关键词（如报错类型、模块名）。",
		func(ctx context.Context, in *similarIn) (*similarOut, error) {
			res, _, serr := sm.SearchIssues(ctx, provider.SearchIssuesOptions{
				Query: in.Keywords, Repo: owner + "/" + repo, PerPage: 10,
			})
			if serr != nil {
				return &similarOut{Error: plugin.ToolErrNote(serr)}, nil
			}
			items := make([]similarItem, 0, len(res))
			for _, r := range res {
				if r == nil {
					continue
				}
				items = append(items, similarItem{
					Number: r.Number, Title: r.Title, State: string(r.State),
					URL: r.WebURL, Labels: r.Labels,
				})
			}
			return &similarOut{Issues: items}, nil
		})
	if terr != nil {
		return nil, terr
	}
	return si, nil
}

// resolveWorkcopyDir 实现 Engine.workcopyDir（PR 工作副本就绪，30s 预算）。
func resolveWorkcopyDir(e *Engine, ctx context.Context, platform, owner, repo, number string) (string, bool) {
	if e.WC == nil || e.SDK == nil {
		// 与 searchCode 的 WC nil 守卫同口径：工具面在未装配工作副本/SDK 的
		// 测试/裁剪装配下应返回「不可用」，不能解引用空指针。
		e.log().Warn("argus.qa.workcopy_unavailable", "reason", "WC 或 SDK 未装配")
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, qaWorkcopyBudget)
	defer cancel()
	headSHA := ""
	if cr, err := e.SDK.GetCR(cctx, platform, owner, repo, number); err == nil && cr != nil {
		headSHA = cr.HeadSHA
	}
	key := workcopy.WorktreeKey{Platform: platform, Owner: owner, Repo: repo, Number: number, HeadSHA: headSHA}
	dir, err := e.WC.Ensure(cctx, key)
	if err != nil {
		e.log().Warn("argus.qa.workcopy_failed", "error", err.Error())
		return "", false
	}
	e.WC.Release(key)
	return dir, true
}

// buildSearchCodeTool 实现 Engine.newSearchCodeTool（search_code 构造单源）。
func buildSearchCodeTool(e *Engine, fetch func(ctx context.Context) string) tool.BaseTool {
	st, err := toolutils.InferTool("search_code", codesearch.ToolDescription,
		func(ctx context.Context, in *codesearch.ToolIn) (*codesearch.ToolOut, error) {
			d := fetch(ctx)
			if d == "" {
				return &codesearch.ToolOut{Result: plugin.ReminderNote(plugin.NoteToolError, "工作副本不可用（冷克隆超时或失败），本轮无法全文检索，请改用 get_file 按路径核实")}, nil
			}
			return searchCodeExec(ctx, d, in), nil
		})
	if err != nil {
		e.log().Warn("argus.qa.search_tool_build_failed", "error", err.Error())
		return nil
	}
	return st
}

// buildLazySearchCodeTool 实现 Engine.searchCodeTool（惰性 search_code 工具）。
func buildLazySearchCodeTool(e *Engine, platform, owner, repo, number string) tool.BaseTool {
	var once sync.Once
	var dir string
	return e.newSearchCodeTool(func(ctx context.Context) string {
		once.Do(func() {
			if d, ok := e.workcopyDir(ctx, platform, owner, repo, number); ok {
				dir = d
			}
		})
		return dir
	})
}

// searchCode 实现 Engine.SearchCode（MCP search_code 数据面）。
func searchCode(e *Engine, ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	if e.WC == nil || !e.CodeSearch {
		return "", fmt.Errorf("search_code 未启用（ARGUS_CODESEARCH=0 或未配置工作副本池）")
	}
	dir, ok := e.workcopyDir(ctx, platform, owner, repo, number)
	if !ok {
		return "", fmt.Errorf("工作副本不可用（冷克隆超时或失败），可稍后重试或按报告文本核实")
	}
	return searchCodeExec(ctx, dir, &codesearch.ToolIn{Pattern: pattern, Glob: glob, IgnoreCase: ignoreCase}).Result, nil
}
