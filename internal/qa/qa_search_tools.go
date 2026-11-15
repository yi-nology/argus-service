package qa

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	toolutils "github.com/cloudwego/eino/components/tool/utils"
	"github.com/oagentkit/textx/textutil"
	"github.com/oagentkit/tools/workcopy"
	"github.com/yi-nology/argus/internal/codesearch"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/go-git-platform/provider"
)

// similarIssueTool 构造 search_similar_issues（v3.73.0，对标 PR-Agent
// /similar_issue）：仓库内相似 issue 检索——"这个问题之前报过吗/和 #x 是一回事吗"。
// SearchManager 是可选能力，未实现的平台不加该工具（ReAct 面自然收窄），
// 此时返回 (nil, nil)。
func (e *Engine) similarIssueTool(platform, owner, repo string) (tool.BaseTool, error) {
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

// qaWorkcopyBudget 冷克隆预算（QA 交互路径，不拖首答）。
const qaWorkcopyBudget = 30 * time.Second

// workcopyDir 就绪 PR 工作副本（30s 预算；失败静默降级）。池语义：Ensure 后
// 立即 Release（目录由 TTL 兜底存活），search_code 调用期按路径直读。
func (e *Engine) workcopyDir(ctx context.Context, platform, owner, repo, number string) (string, bool) {
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

// searchCodeExec search_code 执行体（惰性/绑定目录两构造器单源；目录由调用方
// 解析，检索、错误回执与超长截断语义在此统一）。
func searchCodeExec(ctx context.Context, dir string, in *codesearch.ToolIn) *codesearch.ToolOut {
	res, serr := codesearch.Search(ctx, dir, in.Pattern,
		codesearch.Options{Globs: codesearch.NormalizeGlobs(in.Glob), IgnoreCase: in.IgnoreCase})
	if serr != nil {
		return &codesearch.ToolOut{Result: plugin.ToolErrNote(serr)}
	}
	text, _ := textutil.TruncRunes(res.Render(), 4000)
	return &codesearch.ToolOut{Result: plugin.DataFence("search_code 检索结果（数据）", text)}
}

// newSearchCodeTool search_code 构造单源（v3.111.0，惰性/绑定目录两形态唯一
// 差异是目录解析时机，fetch 给出目录；空=不可用，返回工具降级文案而非 error——
// ReAct 面自然收窄，与其他工具错误同型）。检索/错误回执/截断语义在
// searchCodeExec 统一。
func (e *Engine) newSearchCodeTool(fetch func(ctx context.Context) string) tool.BaseTool {
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

// searchCodeTool 惰性构造 search_code 工具（v3.91.1）：目录解析（含冷克隆
// 30s 预算）推迟到工具首调，构造期零成本。
func (e *Engine) searchCodeTool(platform, owner, repo, number string) tool.BaseTool {
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

// SearchCode 单次全文检索（v3.121.0，MCP search_code 数据面）：与 ReAct 工具
// 同一执行体（searchCodeExec——检索/错误回执/截断语义单源），目录解析复用
// workcopyDir（30s 冷克隆预算）。未启用或工作副本不可用返回 error（环境面
// 故障，调用方改 pattern 自愈不了）；命中与检索类错误回执都在返回文本内。
func (e *Engine) SearchCode(ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	if e.WC == nil || !e.CodeSearch {
		return "", fmt.Errorf("search_code 未启用（ARGUS_CODESEARCH=0 或未配置工作副本池）")
	}
	dir, ok := e.workcopyDir(ctx, platform, owner, repo, number)
	if !ok {
		return "", fmt.Errorf("工作副本不可用（冷克隆超时或失败），可稍后重试或按报告文本核实")
	}
	return searchCodeExec(ctx, dir, &codesearch.ToolIn{Pattern: pattern, Glob: glob, IgnoreCase: ignoreCase}).Result, nil
}
