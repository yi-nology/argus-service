// writing_ci.go —— CI 失败归因与本地 diff 预审任务入口（自 writing.go 拆出）。

package runner

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

// runCIAnalysis CI 失败归因任务入口（独立于 run 管线：push 的 commit 无 CR，
// 不走 prepareContext）。链路：拉失败 job 日志（provider 可选能力 CILogManager，
// 未实现的平台任务失败降级）→ LLM 归因 → 评论回 PR（有 MR 时）→ 落库收口。
func (r *Runner) runCIAnalysis(ctx context.Context, task *storage.Task, start time.Time) error {
	return runCIAnalysis(r, ctx, task, start)
}

// runDiffPreview 本地 diff 预审任务（v3.123.0）：run 在 prepareContext 之前
// 分派（同 ci_analysis，无 MR 语义）。输入 artifacts["diff"]，经 writing.
// ReviewDiff 单轮执行体（与 MCP review_diff 同步路径 prompt 单源），报告落库
// 供提交方 get_report 轮询，不回帖不进 reportview 审查模板。
func (r *Runner) runDiffPreview(ctx context.Context, task *storage.Task, start time.Time) error {
	return runDiffPreview(r, ctx, task, start)
}
