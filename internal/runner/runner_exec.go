// runner_exec.go —— 任务级回调链与工作流接线（自 runner.go 拆出）。

package runner

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/port"

	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"

	"github.com/oagentkit/llm/obsx"
)

// execContext 注入任务级回调链并返回新 ctx：观测 tracing + 旁路用量记账
// （v0.10.12 记账单源化——obsx OnUsage 退役，trace 与 usage 两个 handler 一次
// InitCallbacks 注入；UsageHandler 只补 ReAct/RawModel 旁路，Generate 直连已由
// llm.Client.OnUsage 记账，handler 内防重跳过，两侧不双计）。旁路用量回灌任务
// 预算（v3.36.0）并记任务级/进程级成本。PreviewLen 刻意留 0（不落消息内容）。
// #32：本 sink 同时是逐调用实销埋点（recordUsageEvent）——全任务唯一的用量
// 汇聚面，Generate 与旁路两条路径都到此，运行中消费即时入账。
func (r *Runner) execContext(ctx context.Context, task *storage.Task, budget *llm.Budget, taskCost *llm.CostTracker) context.Context {
	// 终审 Critical：执行实例 nonce——stale 重跑（ResetRunningToPending 不 bump
	// attempt）同 task+attempt 计数器重启，稳定键无 nonce 会跨执行撞唯一索引
	// 静默少计。必须在闭包（recordUsageEvent 调用点）创建前注入：闭包捕获本地
	// ctx 变量，此处先行赋值即保证闭包可见 nonce。缺席才铸新（上游/测试显式
	// 注入的确定性 nonce 不覆盖）。
	if execNonceFrom(ctx) == "" {
		ctx = withExecNonce(ctx, newExecNonce())
	}
	// Phase1③：任务作用域 LLM 调用计数器——rec.Iteration 随 UsageRecord 进入
	// recordUsageEvent，铸造稳定 invocation_id（同物理调用重复投递撞唯一索引
	// 收敛）。注入必须在 InitCallbacks 之前，使 eino 回调链 ctx 可见。
	ctx = llm.WithCallCounter(ctx)
	obsOpt := obsx.Options{SlowThreshold: 60 * time.Second}
	handlers := []callbacks.Handler{obsx.NewTracingHandler(r.Log, obsOpt)}
	// 用量 handler 装配条件含 Store（#32）：逐调用实销埋点是计费账本、不随
	// 成本指标开关（r.Cost）缺席而失效；Cost 为空时仅跳过进程级成本行。
	if r.Cost != nil || r.Store != nil {
		handlers = append(handlers, llm.NewUsageHandler(func(rec llm.UsageRecord) {
			// v3.94.0：agentkit v0.12 起校准内化为 FitModel 实例槽（主路径自动
			// 校准），原 wrap.RecordUsage 回灌调用点删除。旁路无 stage 语义
			//（RunInfo 仅组件类型），stage 记空、模型按组件类型归账。
			budget.Add(rec.PromptTokens + rec.CompletionTokens)
			// #32 逐调用实销埋点：与预算回灌同点落账（best-effort，写失败只告警，
			// 终态差额兜底补账）——运行中/失败/重试的每次调用都进 usage_event。
			r.recordUsageEvent(ctx, task, rec)
			// v3.150.0：Record → RecordPriced（旧 Record 按 1K 计费已退役，
			// 新接口单位写在类型上=每 1M token；argus 配置侧仍是 per-1K，×1000 换算）。
			rate := r.costRate(rec.Model)
			// v3.180.0 省 token 盲区修复：CachedTokens 是命中前缀缓存的 prompt token
			// （便宜得多），旧记账按全价入账 → 成本高估。摊薄 InputPerM 折扣缓存命中
			// 部分（cacheAdjustedInput），token 计数保持真实。llm v0.17.0 起用
			// RecordPricedCached 让 cached 落进 CostRecord.CachedTokens（CacheHitRatio
			// 可派生）——缓存折扣本就该调用方在 Price 层表达（上游 Price 文档明示），
			// 故 cacheAdjustedInput 保留而非上游内置。
			inputPerM := cacheAdjustedInput(rate[0]*1000, rec.PromptTokens, rec.CachedTokens, r.Cfg.CacheReadRatio())
			price := port.Price{InputPerM: inputPerM, OutputPerM: rate[1] * 1000}
			taskCost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			if r.Cost != nil {
				r.Cost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			}
			// 缓存命中率观测（前缀缓存命中率=cached/prompt）：argus 侧 Prometheus 时序
			// 监控面（Grafana），与上游 CostTracker 的 pull 式 Summary 互补不冗余。
			plugin.ReportLLMCache(rec.Model, rec.CachedTokens, rec.PromptTokens)
		}))
	}
	return callbacks.InitCallbacks(ctx, nil, handlers...)
}

// wireWorkflow 组装任务级工作流并接线任务侧注入：评审记忆索引（dream 导出）/
// 外部工具调用上限/仓库级评审规范（.argus/REVIEW.md）/阶段进度事件。
func (r *Runner) wireWorkflow(ctx context.Context, task *storage.Task, budget *llm.Budget) *requirement.Workflow {
	wf := r.buildWorkflow(budget)
	// 本仓评审记忆索引（v3.35.0）：dream 周期导出，此处装载注入 R3。
	// v5.0.0 文件面退役：内核是唯一路径；未装配（ARGUS_MEMORY_KERNEL 关）=
	// 空串（R3 提示词无该段），功能未启用零开销。
	// 内核召回走八段 + MemoryBand 防伪（sessionID=任务 ID 防回声）。
	if r.MemKernel != nil {
		ref := memorykernel.Ref{Platform: task.Platform, Owner: task.Owner, Repo: task.Repo}
		wf.MemoryIndex = r.MemKernel.RecallIndex(ctx, memorykernel.NamespaceOf(ref),
			memorykernel.OrgNamespace, task.ID, "", r.Cfg.MemoryIndexBudget)
	}
	// 外部工具调用上限可配（v3.41.0）
	wf.ExternalToolCallLimit = r.Cfg.ExternalToolCallLimit
	// 仓库级评审规范（v3.38.0）：.argus/REVIEW.md @ head，R3/R4 注入
	wf.RepoGuide = r.RepoGuideFor(ctx, task.Platform, task.Owner, task.Repo, task.HeadSHA)
	// 阶段进度事件（v3.40.0）：R1/R2/R3/R35 进入时发 task.stage，SSE 消费者可展示。
	// v6.1.5 执行期进度可见化：同一回调里把 start marker 原地更新为带阶段进度条的
	// 「审查中」文案——onStage 每阶段回调恰好一次且 setStartMarker 扫锚点原地更新，
	// 天然节流；完成/失败三态收口（runner_lifecycle.go / writing.go）随后整体覆盖
	// marker，进度版只服务执行期。ctx 直接沿用 wireWorkflow 的任务级 ctx：onStage
	// 由 workflow 在 wf.Run(ctx,…) 的同一请求生命周期内同步回调（requirement.onStage），
	// ctx 必然存活，无须 context.Background()——任务取消时更新随之中止（更新失败
	// 仅 Warn，不阻断审查主链路，纪律由 setStartMarker 既有实现保证）。
	wf.OnStage = func(stage string) {
		r.publishStage(task, stage)
		r.setStartMarker(ctx, task, startMarkerProgressBody(task.ID, stage))
	}
	// 详情存档目录（v5.0.0 spill 收编）：超限工具全文落盘任务目录，get_detail 跨
	// checkpoint 复活/重启取回不失效；顺带 best-effort 清理 7 天前过期任务目录。
	wf.SpillDir = filepath.Join(r.Cfg.WorkRoot, "spill", task.ID)
	return wf
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
