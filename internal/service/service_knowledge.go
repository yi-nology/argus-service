// service_knowledge.go —— 知识库检索面（v3.125.0）：把 R3/R4 审查面同源的
// search_knowledge 数据面暴露给 MCP（行业对标 Greptile custom context / 知识库
// 检索工具——外部 agent 回答「团队规范/历史结论/这条以前怎么裁」的入口）。

package service

import (
	"context"
)

// search_knowledge 的入参钳制与渲染预算（MCP 工具描述里的「缺省 5、上界 10」
// 与本值同源手写，改此须同步）。
const (
	knowledgeTopKDefault = 5
	knowledgeTopKMax     = 10
	knowledgeChunkCap    = 1200 // 单片段渲染截断（rune）
)

// SearchKnowledge 知识库语义检索（MCP search_knowledge 数据面）：与 R3/R4 的
// search_knowledge 工具同源（s.Knowledge 单源）。知识未配置（nil/Noop）返回
// error——与 fix_finding/search_code 的「启用语义运行期守卫」同型，工具不消失。
// 返回围栏文本：知识内容来自组织 markdown 与回流审查结论，按数据围栏纪律防注入。
func (s *Service) SearchKnowledge(ctx context.Context, query string, topK int) (string, error) {
	return searchKnowledge(s, ctx, query, topK)
}

// SearchSem 工作副本语义检索（v3.126.0，MCP search_sem 数据面）：委派
// qa.Engine（工作副本目录解析、入参钳制与渲染单源——SearchCode 同模式）。
func (s *Service) SearchSem(ctx context.Context, platform, owner, repo, number, query string, topK int) (string, error) {
	return searchSem(s, ctx, platform, owner, repo, number, query, topK)
}

// search_findings 的入参钳制与渲染预算（MCP 工具描述「缺省 10、上界 50」同源）。
const (
	findingsHistoryDefault = 10
	findingsHistoryMax     = 50
	historyCommentCap      = 240 // 单条评语渲染截断（rune）
)

// SearchFindingsHistory 本仓历史意见检索（v3.138.0，MCP search_findings 数据面）：
// 「这个问题以前报过吗/怎么裁的/被压过吗」——prints 账本跨轮次检索。仓库白名单
// 双门同其他数据工具；返回围栏文本。
func (s *Service) SearchFindingsHistory(ctx context.Context, platform, owner, repo, query string, limit int) (string, error) {
	return searchFindingsHistory(s, ctx, platform, owner, repo, query, limit)
}

// Trace 两符号间调用链（v5.9.0，MCP get_trace 数据面）：委派 qa.Engine（工作
// 副本目录解析、守卫与渲染单源——SearchSem 同模式）。仓库白名单门同 SearchSem；
// 不可达返回提示文本非 error（正常查询结果），语义细节在 qa.Engine 单源。
func (s *Service) Trace(ctx context.Context, platform, owner, repo, number, from, to string) (string, error) {
	return trace(s, ctx, platform, owner, repo, number, from, to)
}

// SymContext 单符号图上下文（v5.9.0，MCP get_symbol_context 数据面）：委派
// qa.Engine（工作副本目录解析、守卫与渲染单源——SearchSem 同模式）。
func (s *Service) SymContext(ctx context.Context, platform, owner, repo, number, symbol string) (string, error) {
	return symContext(s, ctx, platform, owner, repo, number, symbol)
}
