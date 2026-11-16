// service_suppress.go —— 误报抑制的 MCP 数据面（v3.128.0，scope 透传 admin 门）：
// HTTP /findings/suppress 的写语义复用 SuppressFinding 单源，本文件只补
// 「MCP 工具级权限二次判定」——/mcp 路由鉴权只要 ci 档，而抑制写库进学习环
// 是信任边界操作，必须按工具级 admin 收口（fail-closed：ctx 无 scope 一律拒）。

package service

import (
	"context"
)

// SuppressFindingScoped 单条意见的误报抑制（MCP suppress_finding 数据面）：
// 按 fp 短码解析入库意见 → 置 suppressed → 知识回流 + 学习环负票（SuppressFinding
// 单源执行）。fp 为报告/list_findings 的指纹短码（≥6 位），行号/评语漂移由
// 入库指纹覆盖（模式一语义）。
func (s *Service) SuppressFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	return suppressFindingScoped(s, ctx, platform, owner, repo, number, fp)
}

// RestoreFindingScoped 恢复被抑制的意见（MCP restore_finding / HTTP
// /findings/restore 的数据面，v3.131.0）：suppress 反向操作——置回 open，下一轮
// 起重新可见。与评论命令 "restore fp:xxx" 共用 restoreFindingCore 单源；权限
// 同 suppress（admin scope 二次判定，fail-closed）。
func (s *Service) RestoreFindingScoped(ctx context.Context, platform, owner, repo, number, fp string) (map[string]any, error) {
	return restoreFindingScoped(s, ctx, platform, owner, repo, number, fp)
}

// restoreFindingCore 恢复执行体（评论命令与 API/MCP 共用单源）：短码解析入库
// 身份 → 置回 open → 审计 → 学习环 PatternRestore。不回流知识（恢复只是撤销
// 剔除，不是新结论）。
func (s *Service) restoreFindingCore(ctx context.Context, platform, owner, repo, number, fpPrefix string) (bool, error) {
	return restoreFindingCore(s, ctx, platform, owner, repo, number, fpPrefix)
}
