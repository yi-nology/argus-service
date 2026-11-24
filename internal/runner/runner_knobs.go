// runner_knobs.go —— 任务级按仓旋钮（v3.187.0）：repo_settings 覆盖 → 全局
// 默认的单次加载与合并。Execute 入口读一次（主键点查，失败降级全局默认——
// 配置面故障不阻断审查，与 loadRepoConfig 同纪律），三个生效值随任务体显式
// 传递（Runner 是共享实例，禁止挂实例字段）。

package runner

import (
	"context"

	"github.com/yi-nology/argus/internal/storage"
)

// taskKnobs 单任务生效旋钮（服务侧 repo_settings 覆盖后的合并结果）。
type taskKnobs struct {
	// monthlyBudget 仓库月度 token 预算闸（budgetGate 配额账；0 = 关）。
	monthlyBudget int
	// taskTokenBudget 单任务 token 预算（budget 初值）。
	taskTokenBudget int
	// maxDiffChars diff 总量上限（prepareContext 增量/全量清洗共用）。
	maxDiffChars int
	// reportTemplate 报告契约模板 admin 层覆盖（v6.11.0；空 = 未设置，落
	// .reviewagents.yaml/env 裁决链）。词表复核在消费端（reporttmpl.IsValid）。
	reportTemplate string
	// commentThrottleMin 回复频次：同 PR 上次审查完成后 N 分钟内的新任务跳过
	// 回帖（报告仍落库，任务 done；0 = 不节流）。
	commentThrottleMin int
	// findingsCap 回复数量：单报告 findings 上限（severity 降序 top N；0 = 不封顶）。
	findingsCap int
	// severityFloor 回复级别：地板以下 findings 静默只留计数（空 = 全报）。
	severityFloor string
	// ocrDelegateEnabled OCR 按仓开关（AND 部署 ARGUS_OCR_BIN；false=跳过 delegate）。
	ocrDelegateEnabled bool
}

// loadTaskKnobs 加载并合并：无覆盖行 / 读失败 / 落库值损坏 一律降级全局默认。
func (r *Runner) loadTaskKnobs(ctx context.Context, task *storage.Task) taskKnobs {
	return loadTaskKnobs(r, ctx, task)
}
