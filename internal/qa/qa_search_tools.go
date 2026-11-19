package qa

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/textx/textutil"
	"github.com/yi-nology/argus/internal/codesearch"
	"github.com/yi-nology/argus/internal/plugin"
)

// similarIssueTool 构造 search_similar_issues（v3.73.0，对标 PR-Agent
// /similar_issue）：仓库内相似 issue 检索——"这个问题之前报过吗/和 #x 是一回事吗"。
// SearchManager 是可选能力，未实现的平台不加该工具（ReAct 面自然收窄），
// 此时返回 (nil, nil)。
func (e *Engine) similarIssueTool(platform, owner, repo string) (tool.BaseTool, error) {
	return buildSimilarIssueTool(e, platform, owner, repo)
}

// qaWorkcopyBudget 冷克隆预算（QA 交互路径，不拖首答）。
const qaWorkcopyBudget = 30 * time.Second

// workcopyDir 就绪 PR 工作副本（30s 预算；失败静默降级）。池语义：Ensure 后
// 立即 Release（目录由 TTL 兜底存活），search_code 调用期按路径直读。
func (e *Engine) workcopyDir(ctx context.Context, platform, owner, repo, number string) (string, bool) {
	return resolveWorkcopyDir(e, ctx, platform, owner, repo, number)
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
	return &codesearch.ToolOut{Result: plugin.DataFence(ctx, "search_code 检索结果（数据）", text)}
}

// newSearchCodeTool search_code 构造单源（v3.111.0，惰性/绑定目录两形态唯一
// 差异是目录解析时机，fetch 给出目录；空=不可用，返回工具降级文案而非 error——
// ReAct 面自然收窄，与其他工具错误同型）。检索/错误回执/截断语义在
// searchCodeExec 统一。
func (e *Engine) newSearchCodeTool(fetch func(ctx context.Context) string) tool.BaseTool {
	return buildSearchCodeTool(e, fetch)
}

// searchCodeTool 惰性构造 search_code 工具（v3.91.1）：目录解析（含冷克隆
// 30s 预算）推迟到工具首调，构造期零成本。
func (e *Engine) searchCodeTool(platform, owner, repo, number string) tool.BaseTool {
	return buildLazySearchCodeTool(e, platform, owner, repo, number)
}

// SearchCode 单次全文检索（v3.121.0，MCP search_code 数据面）：与 ReAct 工具
// 同一执行体（searchCodeExec——检索/错误回执/截断语义单源），目录解析复用
// workcopyDir（30s 冷克隆预算）。未启用或工作副本不可用返回 error（环境面
// 故障，调用方改 pattern 自愈不了）；命中与检索类错误回执都在返回文本内。
func (e *Engine) SearchCode(ctx context.Context, platform, owner, repo, number, pattern, glob string, ignoreCase bool) (string, error) {
	return searchCode(e, ctx, platform, owner, repo, number, pattern, glob, ignoreCase)
}
