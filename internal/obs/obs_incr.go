package obs

import "sync/atomic"

// ---------- P2-05（#16）导出计数面 ----------

// IncrAuthzDenied 授权拒绝计数（统一 Authorizer 各出口调用）。
func IncrAuthzDenied(route, scope string) {
	authzDenied.WithLabelValues(route, scope).Inc()
}

// IncrEventDropped 事件丢弃计数（reason: authz_filtered / outbox_failed / bus_overflow）。
func IncrEventDropped(reason string) {
	eventsDropped.WithLabelValues(reason).Inc()
}

// IncrCommentRedelivery 评论投递幂等命中计数。
func IncrCommentRedelivery() {
	commentRedelivery.Inc()
}

// IncrBudgetExceeded 预算超限计数（ledger: quota / real）。
func IncrBudgetExceeded(ledger string) {
	budgetExceeded.WithLabelValues(ledger).Inc()
}

// IncrSideEffectMismatch 副作用不一致检出计数（P2-03 对账面）。
func IncrSideEffectMismatch(n int64) {
	sideEffectMismatch.Add(float64(n))
}

// llmFailoverCount LLM 主备切换的单调计数（P2-04 #15 完整性门禁的降级史输入：
// 工作流 run 前后差值 = 本轮审查经历的降级次数——跨任务并发下差值是上界近似，
// 误伤方向是「宁可 COMMENT 不放不可靠 APPROVE」，语义可接受）。
var llmFailoverCount atomic.Int64

// LLMFailoverTotal LLM 主备切换累计次数（单调）。
func LLMFailoverTotal() int64 { return llmFailoverCount.Load() }

// AddSSEConnections SSE 订阅连接数增减（biz events handler 调用）。
func AddSSEConnections(delta int) {
	sseConnections.Add(float64(delta))
}

// SetTaskQueueDepth 队列深度 gauge（main 的周期采样器调用）。
func SetTaskQueueDepth(pending, running int64) {
	tasksPending.Set(float64(pending))
	tasksRunning.Set(float64(running))
}

// IncrNotifyFailure IM 通知推送失败计数。
func IncrNotifyFailure(channel string) {
	notifyFailures.WithLabelValues(channel).Inc()
}

// IncrWebhookSkipped webhook 事件被规则跳过计数（service 包调用）。
func IncrWebhookSkipped(reason string) {
	webhookSkipped.WithLabelValues(reason).Inc()
}

// IncrWebhookRejected webhook 入口拒绝计数（含安全信号：签名/secret 拒绝）。
func IncrWebhookRejected(reason string) {
	webhookRejected.WithLabelValues(reason).Inc()
}

// IncrWebhookEnsure 启动期 webhook 幂等确保动作计数（bootstrap 调用）。
func IncrWebhookEnsure(action string) {
	webhookEnsure.WithLabelValues(action).Inc()
}

// IncrLLMFailover LLM 主备切换计数（bootstrap 装配的两类切换回调调用）。
func IncrLLMFailover(stage string) {
	llmFailovers.WithLabelValues(stage).Inc()
	llmFailoverCount.Add(1)
}

// IncrCtxFit 上下文窗口拟合事件（ctxfit 装饰器回调；仅非 passthrough 上报）。
func IncrCtxFit(stage, action string) {
	ctxFit.WithLabelValues(stage, action).Inc()
}

// IncrLLMContinued 输出截断续写轮次（ContinueModel 回调）。
func IncrLLMContinued(stage string) {
	llmContinued.WithLabelValues(stage).Inc()
}

// IncrToolAnomaly 循环异常守卫拦截（同名同参连续重复调用）。
func IncrToolAnomaly(tool string) {
	toolAnomaly.WithLabelValues(tool).Inc()
}

// AddCtxBreakdown 上下文构成分解累计（stage×role 的 rune 量）。
func AddCtxBreakdown(stage, role string, runes int) {
	ctxBreakdown.WithLabelValues(stage, role).Add(float64(runes))
}

// IncrR2SoftFailure R2 变更摘要软失败（输出空/过短）。
func IncrR2SoftFailure() {
	r2SoftFailure.Inc()
}

// IncrQAFailure 评论链路处理失败计数（service 层调用）。
func IncrQAFailure(reason string) {
	qaFailures.WithLabelValues(reason).Inc()
}

// IncrPollerFailure 轮询兜底失败计数（poller 包调用）。
func IncrPollerFailure(reason string) {
	pollerFailures.WithLabelValues(reason).Inc()
}

// IncrCommentBusy 评论并发闸满载拒绝计数（service 层调用）。
func IncrCommentBusy() {
	commentBusy.Inc()
}

// IncrTestGenVerify testgen 沙箱验证结果计数（v3.22.0；passed/failed）。
// 验证结果标签词表（word_tables distinct 域：本包另起名字——与任务状态
// 同形不同义，值不共享）。
const (
	verifyLabelPassed = "passed"
	verifyLabelFailed = "failed"
)

func IncrTestGenVerify(passed bool) {
	if passed {
		testGenVerify.WithLabelValues(verifyLabelPassed).Inc()
		return
	}
	testGenVerify.WithLabelValues(verifyLabelFailed).Inc()
}

// IncrFixVerify 修复沙箱验证结果计数（阶段 3：有效率分母）。
func IncrFixVerify(passed bool) {
	if passed {
		fixVerify.WithLabelValues(verifyLabelPassed).Inc()
		return
	}
	fixVerify.WithLabelValues(verifyLabelFailed).Inc()
}

// IncrFixPush 修复推送环结果计数（阶段 3：mode=branch|direct，
// result=pushed/cr_created/cr_failed/apply_failed/commit_failed/push_failed）。
func IncrFixPush(mode, result string) {
	fixPush.WithLabelValues(mode, result).Inc()
}

// IncrSideEffectUnavailable 副作用闸 fail-soft 直通计数（effect 包调用；
// reason=mode_off/no_runner/store_unavailable）。非零 = at-most-once 不再成立。
func IncrSideEffectUnavailable(reason string) {
	sideEffectUnavailable.WithLabelValues(reason).Inc()
}

// IncrSideeffectReconcile 未知态副作用自动对账结果计数（effect.Reconciler 调用；
// result=applied/retryable/skipped/error）。
func IncrSideeffectReconcile(result string) {
	sideEffectReconcile.WithLabelValues(result).Inc()
}

// IncrCommentPostFailure 评论回帖失败计数（service 层调用）。
func IncrCommentPostFailure(kind string) {
	commentPostFailures.WithLabelValues(kind).Inc()
}
