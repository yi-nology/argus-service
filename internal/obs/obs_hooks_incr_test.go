package obs

// Init/导出计数面的行为测试：Init 必须把全部 collector 注册进 metrics.Registry
// 并注入 plugin/storage 观测钩子；每个 Incr* 助手必须落到正确指标与标签上。
//
// 计数断言一律用「读前值→动作→增量」：Prometheus collector 是进程级全局，
// go test -count=N 会重跑同一进程，绝对 ==1 会随累计值漂移。
//
// 租户维度（#110）：本文件主用例跑在 tenant.NewContext("acme") 上——每个
// Incr* 的 tenant label 断言同时就是对「ctx 提取租户」的钉约；default 回落
// 行为在 TestTenantLabelFallbackDefault 单独钉。

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/yi-nology/argus/internal/tenant"
)

func TestIncrHelpersRecordMetrics(t *testing.T) {
	ctx := tenant.NewContext(context.Background(), "acme")

	if got := deltaCounter(t, authzDenied.WithLabelValues("hook-route", "write", "acme"), func() {
		IncrAuthzDenied(ctx, "hook-route", "write")
	}); got != 1 {
		t.Errorf("IncrAuthzDenied delta = %v, want 1", got)
	}

	if got := deltaCounter(t, eventsDropped.WithLabelValues("hook-outbox", "acme"), func() {
		IncrEventDropped(ctx, "hook-outbox")
	}); got != 1 {
		t.Errorf("IncrEventDropped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentRedelivery.WithLabelValues("acme"), func() {
		IncrCommentRedelivery(ctx)
	}); got != 1 {
		t.Errorf("IncrCommentRedelivery delta = %v, want 1", got)
	}

	if got := deltaCounter(t, budgetExceeded.WithLabelValues("hook-real", "acme"), func() {
		IncrBudgetExceeded(ctx, "hook-real")
	}); got != 1 {
		t.Errorf("IncrBudgetExceeded delta = %v, want 1", got)
	}

	if got := deltaCounter(t, sideEffectMismatch.WithLabelValues("acme"), func() {
		IncrSideEffectMismatch(ctx, 3)
	}); got != 3 {
		t.Errorf("IncrSideEffectMismatch delta = %v, want 3", got)
	}

	if got := deltaCounter(t, sideEffectReconcile.WithLabelValues("applied", "acme"), func() {
		IncrSideeffectReconcile(ctx, "applied")
	}); got != 1 {
		t.Errorf("IncrSideeffectReconcile delta = %v, want 1", got)
	}

	if got := deltaCounter(t, sseConnections, func() {
		AddSSEConnections(2)
	}); got != 2 {
		t.Errorf("AddSSEConnections(+2) delta = %v, want 2", got)
	}
	if got := deltaCounter(t, sseConnections, func() {
		AddSSEConnections(-1)
	}); got != -1 {
		t.Errorf("AddSSEConnections(-1) delta = %v, want -1", got)
	}

	// Set 语义是覆盖而非累加，绝对值断言在 -count=N 下同样成立（#110 按租户）。
	SetTenantQueueDepths(map[string][2]int64{"acme": {5, 2}, "default": {1, 0}})
	if got := testutil.ToFloat64(tasksPending.WithLabelValues("acme")); got != 5 {
		t.Errorf("tasksPending{acme} = %v, want 5", got)
	}
	if got := testutil.ToFloat64(tasksRunning.WithLabelValues("acme")); got != 2 {
		t.Errorf("tasksRunning{acme} = %v, want 2", got)
	}
	if got := testutil.ToFloat64(tasksPending.WithLabelValues("default")); got != 1 {
		t.Errorf("tasksPending{default} = %v, want 1", got)
	}
	// 消零语义：下轮全量刷新不含 acme → 其 gauge 归零（陈旧序列不清会让
	// per-tenant 告警永不清除）。
	SetTenantQueueDepths(map[string][2]int64{"default": {1, 0}})
	if got := testutil.ToFloat64(tasksPending.WithLabelValues("acme")); got != 0 {
		t.Errorf("tasksPending{acme} 消零后 = %v, want 0", got)
	}

	if got := deltaCounter(t, notifyFailures.WithLabelValues("hook-channel", "acme"), func() {
		IncrNotifyFailure(ctx, "hook-channel")
	}); got != 1 {
		t.Errorf("IncrNotifyFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookSkipped.WithLabelValues("hook-draft", "acme"), func() {
		IncrWebhookSkipped(ctx, "hook-draft")
	}); got != 1 {
		t.Errorf("IncrWebhookSkipped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookRejected.WithLabelValues("hook-signature", "acme"), func() {
		IncrWebhookRejected(ctx, "hook-signature")
	}); got != 1 {
		t.Errorf("IncrWebhookRejected delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookEnsure.WithLabelValues("hook-created", "acme"), func() {
		IncrWebhookEnsure(ctx, "hook-created")
	}); got != 1 {
		t.Errorf("IncrWebhookEnsure delta = %v, want 1", got)
	}

	beforeAtomic := LLMFailoverTotal()
	if got := deltaCounter(t, llmFailovers.WithLabelValues("hook-r3", "acme"), func() {
		IncrLLMFailover(ctx, "hook-r3")
	}); got != 1 {
		t.Errorf("IncrLLMFailover metric delta = %v, want 1", got)
	}
	if got := LLMFailoverTotal(); got != beforeAtomic+1 {
		t.Errorf("LLMFailoverTotal = %d, want %d（原子单调计数）", got, beforeAtomic+1)
	}

	if got := deltaCounter(t, ctxFit.WithLabelValues("hook-r2", "trim", "acme"), func() {
		IncrCtxFit(ctx, "hook-r2", "trim")
	}); got != 1 {
		t.Errorf("IncrCtxFit delta = %v, want 1", got)
	}

	if got := deltaCounter(t, llmContinued.WithLabelValues("hook-r3", "acme"), func() {
		IncrLLMContinued(ctx, "hook-r3")
	}); got != 1 {
		t.Errorf("IncrLLMContinued delta = %v, want 1", got)
	}

	if got := deltaCounter(t, toolAnomaly.WithLabelValues("hook-search", "acme"), func() {
		IncrToolAnomaly(ctx, "hook-search")
	}); got != 1 {
		t.Errorf("IncrToolAnomaly delta = %v, want 1", got)
	}

	if got := deltaCounter(t, ctxBreakdown.WithLabelValues("hook-r3", "user", "acme"), func() {
		AddCtxBreakdown(ctx, "hook-r3", "user", 120)
	}); got != 120 {
		t.Errorf("AddCtxBreakdown delta = %v, want 120", got)
	}

	if got := deltaCounter(t, r2SoftFailure.WithLabelValues("acme"), func() {
		IncrR2SoftFailure(ctx)
	}); got != 1 {
		t.Errorf("IncrR2SoftFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, qaFailures.WithLabelValues("hook-llm", "acme"), func() {
		IncrQAFailure(ctx, "hook-llm")
	}); got != 1 {
		t.Errorf("IncrQAFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, pollerFailures.WithLabelValues("hook-list", "acme"), func() {
		IncrPollerFailure(ctx, "hook-list")
	}); got != 1 {
		t.Errorf("IncrPollerFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentBusy.WithLabelValues("acme"), func() {
		IncrCommentBusy(ctx)
	}); got != 1 {
		t.Errorf("IncrCommentBusy delta = %v, want 1", got)
	}

	if got := deltaCounter(t, testGenVerify.WithLabelValues("passed", "acme"), func() {
		IncrTestGenVerify(ctx, true)
	}); got != 1 {
		t.Errorf("IncrTestGenVerify(passed) delta = %v, want 1", got)
	}
	if got := deltaCounter(t, testGenVerify.WithLabelValues("failed", "acme"), func() {
		IncrTestGenVerify(ctx, false)
	}); got != 1 {
		t.Errorf("IncrTestGenVerify(failed) delta = %v, want 1", got)
	}

	if got := deltaCounter(t, fixVerify.WithLabelValues("passed", "acme"), func() {
		IncrFixVerify(ctx, true)
	}); got != 1 {
		t.Errorf("IncrFixVerify(passed) delta = %v, want 1", got)
	}
	if got := deltaCounter(t, fixVerify.WithLabelValues("failed", "acme"), func() {
		IncrFixVerify(ctx, false)
	}); got != 1 {
		t.Errorf("IncrFixVerify(failed) delta = %v, want 1", got)
	}

	if got := deltaCounter(t, fixPush.WithLabelValues("hook-branch", "hook-cr-created", "acme"), func() {
		IncrFixPush(ctx, "hook-branch", "hook-cr-created")
	}); got != 1 {
		t.Errorf("IncrFixPush delta = %v, want 1", got)
	}

	if got := deltaCounter(t, sideEffectUnavailable.WithLabelValues("hook-mode-off", "acme"), func() {
		IncrSideEffectUnavailable(ctx, "hook-mode-off")
	}); got != 1 {
		t.Errorf("IncrSideEffectUnavailable delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentPostFailures.WithLabelValues("hook-reply", "acme"), func() {
		IncrCommentPostFailure(ctx, "hook-reply")
	}); got != 1 {
		t.Errorf("IncrCommentPostFailure delta = %v, want 1", got)
	}
}

// TestTenantLabelFallbackDefault：无租户声明的 ctx（含 nil）一律落 default 序列
// ——fail-closed 方向与 tenant.FromContext 语义对齐（#110 验收：存量调用零声明
// 不产生新序列爆炸）。
func TestTenantLabelFallbackDefault(t *testing.T) {
	if got := deltaCounter(t, commentPostFailures.WithLabelValues("hook-reply", "default"), func() {
		IncrCommentPostFailure(nil, "hook-reply") //nolint:staticcheck // SA1012  nil ctx 语义即本用例
	}); got != 1 {
		t.Errorf("nil ctx 回落 default：delta = %v, want 1", got)
	}
	if got := deltaCounter(t, commentPostFailures.WithLabelValues("hook-reply", "default"), func() {
		IncrCommentPostFailure(context.Background(), "hook-reply")
	}); got != 1 {
		t.Errorf("无声明 ctx 回落 default：delta = %v, want 1", got)
	}
}
