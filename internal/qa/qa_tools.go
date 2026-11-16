// qa_tools.go —— Q&A 工具装配面：diff/file/similar/search_code 工具集 + 工作副本目录管理。

package qa

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
)

// qa 工具入出参（具名类型——InferTool 泛型推断需要）。

type similarIn struct {
	Keywords string `json:"keywords" jsonschema:"description=检索关键词（报错类型/模块名）"`
}

type similarItem struct {
	Number string   `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state,omitempty"`
	URL    string   `json:"url,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

type similarOut struct {
	Issues []similarItem `json:"issues,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// searchManager 取平台 SearchManager 可选能力（type assertion；未实现返回 false）。
func searchManager(sdk *argusctx.SDKCtx, platform string) (provider.SearchManager, bool) {
	p, err := sdk.Provider(platform)
	if err != nil {
		return nil, false
	}
	sm, ok := p.(provider.SearchManager)
	return sm, ok
}

// tools Q&A 工具表（复用 R3 同源纪律：内容截断 + 数据区围栏防注入）。
func (e *Engine) tools(ctx context.Context, platform, owner, repo, number string) ([]tool.BaseTool, string, error) {
	return buildTools(e, ctx, platform, owner, repo, number)
}

// diffFileTools 构造 get_diff + get_file 两个核心工具（PR 文本面）。
// 工具错误回注（提醒信封）不中断 ReAct；内容截断与行号前缀语义在此统一。
func (e *Engine) diffFileTools(ctx context.Context, platform, owner, repo, number string) (tool.BaseTool, tool.BaseTool, error) {
	return buildDiffFileTools(e, ctx, platform, owner, repo, number)
}
