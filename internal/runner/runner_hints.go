// runner_hints.go —— 学习环误报提示与跨轮意见注入（自 runner_report.go 拆出）。

package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/oagentkit/textx/textutil"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// reviewHints 渲染本仓库历史高误报模式提示词（学习环关闭或无样本时返回空）。
// 失败只降级为空，不阻塞审查。
func (r *Runner) reviewHints(ctx context.Context, pr *plugin.PRContext) string {
	if pr == nil || !r.Learning.Enabled || r.Store == nil {
		return ""
	}
	list, err := r.Store.ListLowConfidencePatterns(ctx, pr.Platform, pr.Owner, pr.Repo,
		r.Learning.MinSamples, r.Learning.InjectFloor, r.Learning.InjectTopN)
	if err != nil || len(list) == 0 {
		if err != nil {
			r.Log.Warn("argus.regret.hints_failed", "error", err.Error())
		}
		return ""
	}
	var b strings.Builder
	b.WriteString("本仓库历史上以下审查模式误报率偏高（开发者 suppress / 终审剔除）。")
	b.WriteString("命中类似表述时：给出具体行与影响，否则不要输出。\n")
	for _, p := range list {
		sample := p.PatternText
		if sample == "" {
			sample = p.Pattern
		}
		fmt.Fprintf(&b, "- [%s] %s（置信度 %.2f，samples=%d）\n", p.Plugin, sample, p.Confidence, p.Samples)
	}
	out := b.String()
	// 注入留痕（可观测性补齐）：提示词面注入此前零日志——线上排查「为什么这轮
	// 意见变少」时无法区分提示词收敛与模型随机性。
	r.Log.Info("argus.regret.hints_injected", "pr", fmt.Sprintf("%s/%s/%s#%s",
		pr.Platform, pr.Owner, pr.Repo, pr.Number), "patterns", len(list), "bytes", len(out))
	return out
}

// prevFindingsSection 渲染同 PR 此前轮次的 open 意见清单（跨轮一致性参照，
// v3.52.0）：file:line [SEVERITY] 评语（单条截 160 rune），上限 15 条防提示词
// 膨胀。查询失败返回空（best-effort，不影响主流程）。
func (r *Runner) prevFindingsSection(ctx context.Context, pr *plugin.PRContext) string {
	if r.Store == nil {
		return ""
	}
	prints, err := r.Store.ListFindingPrints(ctx, pr.Platform, pr.Owner, pr.Repo, pr.Number)
	if err != nil {
		r.Log.Warn("argus.runner.prev_findings_failed", "error", err.Error())
		return ""
	}
	lines := make([]string, 0, len(prints))
	for _, p := range prints {
		if p.Status != storage.PrintOpen || p.Comment == "" || p.File == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- %s:%d [%s] %s",
			p.File, p.Line, strings.ToUpper(p.Severity), textutil.TruncEllipsis(p.Comment, 160)))
		if len(lines) >= 15 {
			break
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
