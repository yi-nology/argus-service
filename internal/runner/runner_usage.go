// runner_usage.go —— 逐调用实销埋点（#32）：execContext UsageHandler 的落账
// sink——每次 LLM 调用一条 usage_event（invocation_id 唯一键幂等），运行中
// 消费即时入账、崩溃窗口收敛到「调用返回→写库」的毫秒级。

package runner

import (
	"context"

	"github.com/oagentkit/llm"
	"github.com/oekit/pkg/stringx"

	"github.com/yi-nology/argus/internal/storage"
)

// execNonceKey 执行实例 nonce 的 ctx 键（未导出：仅 runner 包内装配/读取）。
type execNonceKey struct{}

// withExecNonce 把执行实例 nonce 注入 ctx。每次 Execute 装配期（execContext）
// 注入一个新 nonce，同 task+attempt 的 stale 重跑（ResetRunningToPending 不
// bump attempt）靠它区分执行实例，防稳定 invocation_id 跨执行撞唯一索引。
func withExecNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, execNonceKey{}, nonce)
}

// execNonceFrom 读执行实例 nonce；缺席（直接测试调用等）返回空串。
func execNonceFrom(ctx context.Context) string {
	v, _ := ctx.Value(execNonceKey{}).(string)
	return v
}

// newExecNonce 铸 8 hex chars 执行实例 nonce（uuid 首段即 8 hex，crypto/rand 源）。
func newExecNonce() string {
	return stringx.UUIDv4()[:8]
}

// recordUsageEvent 单次 LLM 调用落账（#32 验收①调用点埋点）。
//
// 埋点层选点：execContext 的 llm.NewUsageHandler sink——它是全任务唯一的
// 用量汇聚面（Generate 直连与 ReAct/RawModel 旁路两条路径都到此，llm 侧
// clientAccounted 防重保证同笔物理调用只投递一次），任务上下文（task/attempt/
// 仓库坐标）在 Execute 装配期即在闭包内，无须再穿 context。
//
// 纪律：
//   - invocation_id 稳定键（Phase1③，终审 Critical 修正）：任务作用域计数器
//     （execContext 注入 llm.WithCallCounter）的 Iteration 铸
//     u-<taskID>-<attempt>-<exec-nonce>-<iteration>——同一物理调用重复投递
//     （回调重放/写库重试）得同键，由 usage_events 唯一索引幂等收敛；执行
//     nonce 区分执行实例（stale 重跑 attempt 不变、计数器重启，无 nonce 会
//     跨执行撞键静默少计）。生产唯一调用点是 execContext 闭包（runner.go），
//     其在闭包创建前注入 nonce——生产路径 nonce 恒在；无 nonce（直接测试
//     调用）退回旧 u-<taskID>-<attempt>-<iteration> 格式保持既有测试语义。
//     Iteration=0（计数器缺席的旁路）退回新 uuid（旧语义），「不可重复计费」
//     单源仍在存储层；
//   - 同步落账 + WithoutCancel：调用已发生、token 已花——任务取消/停机不得
//     抹掉这笔账；5s 超时上界防停机路径挂死（WithoutCancel 保留 ctx 值，
//     nonce 读取发生在其前，互不影响）；
//   - 写失败只告警不返回（sink 契约）：账本绝不阻断审查，丢笔由任务终态的
//     差额兜底（storage.backfillTaskUsageDelta）补账。
func (r *Runner) recordUsageEvent(ctx context.Context, task *storage.Task, rec llm.UsageRecord) {
	recordUsageEvent(r, ctx, task, rec)
}

// usageProvider 调用方 provider 标注：取 LLM BaseURL 主机名（argus 全链路
// OpenAI 兼容端点，端点即 provider 身份——自建网关/mimo 等以主机区分），
// 解析失败缺省 openai。
func (r *Runner) usageProvider() string {
	return usageProvider(r)
}

// estimatedUsageCost 单次调用成本估算（美元）：与报告成本面同口径——输入价
// 按缓存命中折扣摊薄（cacheAdjustedInput），输出价原价；未配价（rate 全 0）
// 返回 0（与「0 = 不计价」配置语义一致）。rate 单位 per-1K（配置面），换算
// 到 per-1M 与 llm.RecordPricedCached 对齐。
func (r *Runner) estimatedUsageCost(rec llm.UsageRecord) float64 {
	return estimatedUsageCost(r, rec)
}
