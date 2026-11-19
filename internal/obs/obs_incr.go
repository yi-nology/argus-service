package obs

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// ---------- P2-05（#16）导出计数面 ----------
// 全部 Incr*/Set* 自 #110 起首参 ctx：tenant label 由 tenant.FromContext 提取
// （nil ctx 回落 default，nil 安全），调用点不传租户字符串、零语义税。

// IncrAuthzDenied 授权拒绝计数（统一 Authorizer 各出口调用）。
func IncrAuthzDenied(ctx context.Context, route, scope string) {
	authzDenied.WithLabelValues(route, scope, tenant.FromContext(ctx)).Inc()
}

// IncrEventDropped 事件丢弃计数（reason: authz_filtered / outbox_failed / bus_overflow）。
func IncrEventDropped(ctx context.Context, reason string) {
	eventsDropped.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrCommentRedelivery 评论投递幂等命中计数。
func IncrCommentRedelivery(ctx context.Context) {
	commentRedelivery.WithLabelValues(tenant.FromContext(ctx)).Inc()
}

// IncrBudgetExceeded 预算超限计数（ledger: quota / real）。
func IncrBudgetExceeded(ctx context.Context, ledger string) {
	budgetExceeded.WithLabelValues(ledger, tenant.FromContext(ctx)).Inc()
}

// IncrSideEffectMismatch 副作用不一致检出计数（P2-03 对账面）。
func IncrSideEffectMismatch(ctx context.Context, n int64) {
	sideEffectMismatch.WithLabelValues(tenant.FromContext(ctx)).Add(float64(n))
}

// llmFailoverCount LLM 主备切换的单调计数（P2-04 #15 完整性门禁的降级史输入：
// 工作流 run 前后差值 = 本轮审查经历的降级次数——跨任务并发下差值是上界近似，
// 误伤方向是「宁可 COMMENT 不放不可靠 APPROVE」，语义可接受）。
var llmFailoverCount atomic.Int64

// LLMFailoverTotal LLM 主备切换累计次数（单调）。
func LLMFailoverTotal() int64 { return llmFailoverCount.Load() }

// AddSSEConnections SSE 订阅连接数增减（biz events handler 调用；服务端全局态）。
func AddSSEConnections(delta int) {
	sseConnections.Add(float64(delta))
}

// tenantDepths 已置数的租户集合（gauge 消零用：租户消失须归零，否则陈旧序列
// 在 Prometheus 里永续滞留，per-tenant 告警永不清除）。
var tenantDepths sync.Map // tenant -> struct{}

// SetTenantQueueDepths 按租户全量刷新队列深度 gauge：map 外的已见租户归零。
// depths[tenant] = [pending, running]。采样器每轮调一次。
func SetTenantQueueDepths(depths map[string][2]int64) {
	seen := make(map[string]bool, len(depths))
	for t, d := range depths {
		seen[t] = true
		tenantDepths.Store(t, struct{}{})
		tasksPending.WithLabelValues(t).Set(float64(d[0]))
		tasksRunning.WithLabelValues(t).Set(float64(d[1]))
	}
	tenantDepths.Range(func(k, _ any) bool {
		t := k.(string)
		if !seen[t] {
			tenantDepths.Delete(t)
			tasksPending.WithLabelValues(t).Set(0)
			tasksRunning.WithLabelValues(t).Set(0)
		}
		return true
	})
}

// IncrNotifyFailure IM 通知推送失败计数。
func IncrNotifyFailure(ctx context.Context, channel string) {
	notifyFailures.WithLabelValues(channel, tenant.FromContext(ctx)).Inc()
}

// IncrWebhookSkipped webhook 事件被规则跳过计数（service 包调用）。
func IncrWebhookSkipped(ctx context.Context, reason string) {
	webhookSkipped.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrWebhookRejected webhook 入口拒绝计数（安全信号）。
func IncrWebhookRejected(ctx context.Context, reason string) {
	webhookRejected.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrWebhookEnsure 启动期幂等确保动作分布。
func IncrWebhookEnsure(ctx context.Context, action string) {
	webhookEnsure.WithLabelValues(action, tenant.FromContext(ctx)).Inc()
}

// IncrLLMFailover LLM 主备切换计数（atomic 单调面 + prometheus 租户面双写：
// 前者供完整性门禁差值判定（LLMFailoverTotal），后者供按租户观测）。
func IncrLLMFailover(ctx context.Context, stage string) {
	llmFailoverCount.Add(1)
	llmFailovers.WithLabelValues(stage, tenant.FromContext(ctx)).Inc()
}

// IncrCtxFit 上下文拟合计数（stage×action）。
func IncrCtxFit(ctx context.Context, stage, action string) {
	ctxFit.WithLabelValues(stage, action, tenant.FromContext(ctx)).Inc()
}

// IncrLLMContinued 输出截断续写计数。
func IncrLLMContinued(ctx context.Context, stage string) {
	llmContinued.WithLabelValues(stage, tenant.FromContext(ctx)).Inc()
}

// IncrToolAnomaly ReAct 异常守卫拦截计数。
func IncrToolAnomaly(ctx context.Context, tool string) {
	toolAnomaly.WithLabelValues(tool, tenant.FromContext(ctx)).Inc()
}

// IncrR2SoftFailure R2 变更摘要软失败计数。
func IncrR2SoftFailure(ctx context.Context) {
	r2SoftFailure.WithLabelValues(tenant.FromContext(ctx)).Inc()
}

// AddCtxBreakdown 上下文构成分解累计（stage×role 的 rune 量）。
func AddCtxBreakdown(ctx context.Context, stage, role string, runes int) {
	ctxBreakdown.WithLabelValues(stage, role, tenant.FromContext(ctx)).Add(float64(runes))
}

// IncrQAFailure 评论链路失败计数（reason: llm/reject/…）。
func IncrQAFailure(ctx context.Context, reason string) {
	qaFailures.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrPollerFailure 轮询兜底失败计数。
func IncrPollerFailure(ctx context.Context, reason string) {
	pollerFailures.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrCommentBusy 评论并发闸满载拒绝。
func IncrCommentBusy(ctx context.Context) {
	commentBusy.WithLabelValues(tenant.FromContext(ctx)).Inc()
}

// IncrTestGenVerify testgen 沙箱验证结果。
func IncrTestGenVerify(ctx context.Context, passed bool) {
	testGenVerify.WithLabelValues(boolLabel(passed), tenant.FromContext(ctx)).Inc()
}

// IncrFixVerify 修复沙箱验证结果。
func IncrFixVerify(ctx context.Context, passed bool) {
	fixVerify.WithLabelValues(boolLabel(passed), tenant.FromContext(ctx)).Inc()
}

// IncrFixPush 修复推送环结果。
func IncrFixPush(ctx context.Context, mode, result string) {
	fixPush.WithLabelValues(mode, result, tenant.FromContext(ctx)).Inc()
}

// IncrSideEffectUnavailable 副作用闸 fail-soft 直通计数。
func IncrSideEffectUnavailable(ctx context.Context, reason string) {
	sideEffectUnavailable.WithLabelValues(reason, tenant.FromContext(ctx)).Inc()
}

// IncrSideeffectReconcile 未知态副作用自动对账结果。
func IncrSideeffectReconcile(ctx context.Context, result string) {
	sideEffectReconcile.WithLabelValues(result, tenant.FromContext(ctx)).Inc()
}

// IncrCommentPostFailure 评论回帖失败计数。
func IncrCommentPostFailure(ctx context.Context, kind string) {
	commentPostFailures.WithLabelValues(kind, tenant.FromContext(ctx)).Inc()
}

// boolLabel bool → passed/failed 词表（failed 引 storage.Status 词表单源，
// 手抄字面量被 archcheck word-table 门禁拦）。
func boolLabel(ok bool) string {
	if ok {
		return "passed"
	}
	return storage.StatusFailed
}
