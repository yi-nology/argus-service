package obs

// Init/导出计数面的行为测试：Init 必须把全部 collector 注册进 metrics.Registry
// 并注入 plugin/storage 观测钩子；每个 Incr* 助手必须落到正确指标与标签上。
//
// 计数断言一律用「读前值→动作→增量」：Prometheus collector 是进程级全局，
// go test -count=N 会重跑同一进程，绝对 ==1 会随累计值漂移。

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestIncrHelpersRecordMetrics(t *testing.T) {
	if got := deltaCounter(t, authzDenied.WithLabelValues("hook-route", "write"), func() {
		IncrAuthzDenied("hook-route", "write")
	}); got != 1 {
		t.Errorf("IncrAuthzDenied delta = %v, want 1", got)
	}

	if got := deltaCounter(t, eventsDropped.WithLabelValues("hook-outbox"), func() {
		IncrEventDropped("hook-outbox")
	}); got != 1 {
		t.Errorf("IncrEventDropped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentRedelivery, func() {
		IncrCommentRedelivery()
	}); got != 1 {
		t.Errorf("IncrCommentRedelivery delta = %v, want 1", got)
	}

	if got := deltaCounter(t, budgetExceeded.WithLabelValues("hook-real"), func() {
		IncrBudgetExceeded("hook-real")
	}); got != 1 {
		t.Errorf("IncrBudgetExceeded delta = %v, want 1", got)
	}

	if got := deltaCounter(t, sideEffectMismatch, func() {
		IncrSideEffectMismatch(3)
	}); got != 3 {
		t.Errorf("IncrSideEffectMismatch delta = %v, want 3", got)
	}

	if got := deltaCounter(t, sideEffectReconcile.WithLabelValues("applied"), func() {
		IncrSideeffectReconcile("applied")
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

	// Set 语义是覆盖而非累加，绝对值断言在 -count=N 下同样成立。
	SetTaskQueueDepth(5, 2)
	if got := testutil.ToFloat64(tasksPending); got != 5 {
		t.Errorf("tasksPending = %v, want 5", got)
	}
	if got := testutil.ToFloat64(tasksRunning); got != 2 {
		t.Errorf("tasksRunning = %v, want 2", got)
	}

	if got := deltaCounter(t, notifyFailures.WithLabelValues("hook-channel"), func() {
		IncrNotifyFailure("hook-channel")
	}); got != 1 {
		t.Errorf("IncrNotifyFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookSkipped.WithLabelValues("hook-draft"), func() {
		IncrWebhookSkipped("hook-draft")
	}); got != 1 {
		t.Errorf("IncrWebhookSkipped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookRejected.WithLabelValues("hook-signature"), func() {
		IncrWebhookRejected("hook-signature")
	}); got != 1 {
		t.Errorf("IncrWebhookRejected delta = %v, want 1", got)
	}

	if got := deltaCounter(t, webhookEnsure.WithLabelValues("hook-created"), func() {
		IncrWebhookEnsure("hook-created")
	}); got != 1 {
		t.Errorf("IncrWebhookEnsure delta = %v, want 1", got)
	}

	beforeAtomic := LLMFailoverTotal()
	if got := deltaCounter(t, llmFailovers.WithLabelValues("hook-r3"), func() {
		IncrLLMFailover("hook-r3")
	}); got != 1 {
		t.Errorf("IncrLLMFailover metric delta = %v, want 1", got)
	}
	if got := LLMFailoverTotal(); got != beforeAtomic+1 {
		t.Errorf("LLMFailoverTotal = %d, want %d（原子单调计数）", got, beforeAtomic+1)
	}

	if got := deltaCounter(t, ctxFit.WithLabelValues("hook-r2", "trim"), func() {
		IncrCtxFit("hook-r2", "trim")
	}); got != 1 {
		t.Errorf("IncrCtxFit delta = %v, want 1", got)
	}

	if got := deltaCounter(t, llmContinued.WithLabelValues("hook-r3"), func() {
		IncrLLMContinued("hook-r3")
	}); got != 1 {
		t.Errorf("IncrLLMContinued delta = %v, want 1", got)
	}

	if got := deltaCounter(t, toolAnomaly.WithLabelValues("hook-search"), func() {
		IncrToolAnomaly("hook-search")
	}); got != 1 {
		t.Errorf("IncrToolAnomaly delta = %v, want 1", got)
	}

	if got := deltaCounter(t, ctxBreakdown.WithLabelValues("hook-r3", "user"), func() {
		AddCtxBreakdown("hook-r3", "user", 120)
	}); got != 120 {
		t.Errorf("AddCtxBreakdown delta = %v, want 120", got)
	}

	if got := deltaCounter(t, r2SoftFailure, func() {
		IncrR2SoftFailure()
	}); got != 1 {
		t.Errorf("IncrR2SoftFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, qaFailures.WithLabelValues("hook-llm"), func() {
		IncrQAFailure("hook-llm")
	}); got != 1 {
		t.Errorf("IncrQAFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, pollerFailures.WithLabelValues("hook-list"), func() {
		IncrPollerFailure("hook-list")
	}); got != 1 {
		t.Errorf("IncrPollerFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentBusy, func() {
		IncrCommentBusy()
	}); got != 1 {
		t.Errorf("IncrCommentBusy delta = %v, want 1", got)
	}

	if got := deltaCounter(t, testGenVerify.WithLabelValues("passed"), func() {
		IncrTestGenVerify(true)
	}); got != 1 {
		t.Errorf("IncrTestGenVerify(passed) delta = %v, want 1", got)
	}
	if got := deltaCounter(t, testGenVerify.WithLabelValues("failed"), func() {
		IncrTestGenVerify(false)
	}); got != 1 {
		t.Errorf("IncrTestGenVerify(failed) delta = %v, want 1", got)
	}

	if got := deltaCounter(t, fixVerify.WithLabelValues("passed"), func() {
		IncrFixVerify(true)
	}); got != 1 {
		t.Errorf("IncrFixVerify(passed) delta = %v, want 1", got)
	}
	if got := deltaCounter(t, fixVerify.WithLabelValues("failed"), func() {
		IncrFixVerify(false)
	}); got != 1 {
		t.Errorf("IncrFixVerify(failed) delta = %v, want 1", got)
	}

	if got := deltaCounter(t, fixPush.WithLabelValues("hook-branch", "hook-cr-created"), func() {
		IncrFixPush("hook-branch", "hook-cr-created")
	}); got != 1 {
		t.Errorf("IncrFixPush delta = %v, want 1", got)
	}

	if got := deltaCounter(t, sideEffectUnavailable.WithLabelValues("hook-mode-off"), func() {
		IncrSideEffectUnavailable("hook-mode-off")
	}); got != 1 {
		t.Errorf("IncrSideEffectUnavailable delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentPostFailures.WithLabelValues("hook-reply"), func() {
		IncrCommentPostFailure("hook-reply")
	}); got != 1 {
		t.Errorf("IncrCommentPostFailure delta = %v, want 1", got)
	}
}
