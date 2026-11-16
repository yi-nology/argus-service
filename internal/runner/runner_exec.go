// runner_exec.go —— 任务级回调链与工作流接线（自 runner.go 拆出）。

package runner

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// execContext 注入任务级回调链并返回新 ctx：观测 tracing + 旁路用量记账
// （v0.10.12 记账单源化——obsx OnUsage 退役，trace 与 usage 两个 handler 一次
// InitCallbacks 注入；UsageHandler 只补 ReAct/RawModel 旁路，Generate 直连已由
// llm.Client.OnUsage 记账，handler 内防重跳过，两侧不双计）。旁路用量回灌任务
// 预算（v3.36.0）并记任务级/进程级成本。PreviewLen 刻意留 0（不落消息内容）。
// #32：本 sink 同时是逐调用实销埋点（recordUsageEvent）——全任务唯一的用量
// 汇聚面，Generate 与旁路两条路径都到此，运行中消费即时入账。
func (r *Runner) execContext(ctx context.Context, task *storage.Task, budget *llm.Budget, taskCost *llm.CostTracker) context.Context {
	return execContext(r, ctx, task, budget, taskCost)
}

// wireWorkflow 组装任务级工作流并接线任务侧注入：评审记忆索引（dream 导出）/
// 外部工具调用上限/仓库级评审规范（.argus/REVIEW.md）/阶段进度事件。
func (r *Runner) wireWorkflow(ctx context.Context, task *storage.Task, budget *llm.Budget) *requirement.Workflow {
	return wireWorkflow(r, ctx, task, budget)
}

// PruneSpillRoot spill 存档过期清理（best-effort，v5.1.0 自每任务 wireWorkflow
// 挪 bootstrap 周期 ticker——每任务全目录扫描在任务风暴下是 O(N²) 面；周期化后
// 扫描频率与任务量解耦）。并发双删无害（后者报错忽略）。
func PruneSpillRoot(root string) {
	dir := filepath.Join(root, "spill")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -7)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}
