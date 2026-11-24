// service_findings.go —— 意见治理台（v8.5.0）：按仓跨 PR 浏览 finding_prints
// （读面）。抑制/恢复写面仍在 service_suppress.go（admin 档 + 仓库维度）。

package service

import "context"

// ListFindings 意见治理台清单：按仓列 finding_prints（最近出现优先），
// status 空=全部、open|suppressed 过滤；limit 缺省 100、上限 500。
// read 档 + 仓库维度判定（越权 403）；响应动态 JSON（findings/counts）。
func (s *Service) ListFindings(ctx context.Context, platform, owner, repo, status string, limit int) (map[string]any, int) {
	return listFindings(s, ctx, platform, owner, repo, status, limit)
}
