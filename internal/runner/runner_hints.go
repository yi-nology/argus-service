// runner_hints.go —— 学习环误报提示与跨轮意见注入（自 runner_report.go 拆出）。

package runner

import (
	"context"

	"github.com/yi-nology/argus/internal/plugin"
)

// reviewHints 渲染本仓库历史高误报模式提示词（学习环关闭或无样本时返回空）。
// 失败只降级为空，不阻塞审查。
func (r *Runner) reviewHints(ctx context.Context, pr *plugin.PRContext) string {
	return reviewHints(r, ctx, pr)
}

// prevFindingsSection 渲染同 PR 此前轮次的 open 意见清单（跨轮一致性参照，
// v3.52.0）：file:line [SEVERITY] 评语（单条截 160 rune），上限 15 条防提示词
// 膨胀。查询失败返回空（best-effort，不影响主流程）。
func (r *Runner) prevFindingsSection(ctx context.Context, pr *plugin.PRContext) string {
	return prevFindingsSection(r, ctx, pr)
}
