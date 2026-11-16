package obs

// Init/导出计数面的行为测试：Init 必须把全部 collector 注册进 metrics.Registry
// 并注入 plugin/storage 观测钩子；每个 Incr* 助手必须落到正确指标与标签上。

import (
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/oekit/metrics"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// obsInitOnce 包级 collector 只能注册一次（MustRegister 重复注册会 panic）。
// sync.Once 同时保证 go test -count=N 重复执行同一进程时测试不炸。
var obsInitOnce sync.Once

func ensureObsInitialized() { obsInitOnce.Do(Init) }

// asCollector HistogramVec.WithLabelValues 返回 Observer，底层样本同时是
// Collector（Metric 接口）；类型断言失败说明采集面形态变了。
func asCollector(t *testing.T, o prometheus.Observer) prometheus.Collector {
	t.Helper()
	c, ok := o.(prometheus.Collector)
	if !ok {
		t.Fatalf("%T 不是 Collector", o)
	}
	return c
}

// histogramSamples 收集一个 histogram 观测面的样本数与样本和。
func histogramSamples(t *testing.T, c prometheus.Collector) (uint64, float64) {
	t.Helper()
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	close(ch)
	var (
		found bool
		count uint64
		sum   float64
	)
	for m := range ch {
		pb := &dto.Metric{}
		if err := m.Write(pb); err != nil {
			t.Fatalf("写 dto 失败: %v", err)
		}
		if pb.Histogram == nil {
			continue
		}
		found = true
		count += pb.GetHistogram().GetSampleCount()
		sum += pb.GetHistogram().GetSampleSum()
	}
	if !found {
		t.Fatal("未收集到 histogram 样本")
	}
	return count, sum
}

func TestInitWiresAllHooks(t *testing.T) {
	ensureObsInitialized()

	hooks := []struct {
		name string
		ok   bool
	}{
		{"HookTaskTotal", plugin.HookTaskTotal != nil},
		{"HookStageDuration", plugin.HookStageDuration != nil},
		{"HookPluginDuration", plugin.HookPluginDuration != nil},
		{"HookPluginFailure", plugin.HookPluginFailure != nil},
		{"HookLLMTokens", plugin.HookLLMTokens != nil},
		{"HookLLMCache", plugin.HookLLMCache != nil},
		{"HookFindings", plugin.HookFindings != nil},
		{"HookFindingsByRepo", plugin.HookFindingsByRepo != nil},
		{"HookTaskDurationByRepo", plugin.HookTaskDurationByRepo != nil},
		{"HookReflectDrop", plugin.HookReflectDrop != nil},
		{"HookReflectDegrade", plugin.HookReflectDegrade != nil},
		{"HookLayerExec", plugin.HookLayerExec != nil},
		{"HookVerdict", plugin.HookVerdict != nil},
		{"HookFenceNeutralized", plugin.HookFenceNeutralized != nil},
		{"HookPluginSkipped", plugin.HookPluginSkipped != nil},
		{"HookCommentRoute", plugin.HookCommentRoute != nil},
		{"HookTaskDeduped", plugin.HookTaskDeduped != nil},
		{"HookBestOfN", plugin.HookBestOfN != nil},
		{"HookMigrationLeaseLost", storage.HookMigrationLeaseLost != nil},
	}
	for _, h := range hooks {
		if !h.ok {
			t.Errorf("Init 未注入 %s", h.name)
		}
	}

	// metrics.Init("argus") 的效应：通用构建信息进入同一 Registry 且 service=argus。
	mfs, err := metrics.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather 失败: %v", err)
	}
	serviceSeen := false
	for _, mf := range mfs {
		if mf.GetName() != "app_build_info" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "service" && l.GetValue() == "argus" {
					serviceSeen = true
				}
			}
		}
	}
	if !serviceSeen {
		t.Fatal("Init 后 Registry 缺少 app_build_info{service=argus}")
	}
}

// TestInitHooksRecordMetrics 逐个驱动钩子并对账指标值/标签——钩子存在不代表
// 埋点接对了线（闭包里写错 collector 会在这里被抓到）。
func TestInitHooksRecordMetrics(t *testing.T) {
	ensureObsInitialized()

	plugin.HookTaskTotal("hookplat", "kind1", "done")
	if got := testutil.ToFloat64(tasksTotal.WithLabelValues("hookplat", "kind1", "done")); got != 1 {
		t.Errorf("HookTaskTotal = %v, want 1", got)
	}

	plugin.HookStageDuration("hook-stage", 1500*time.Millisecond)
	if count, sum := histogramSamples(t, asCollector(t, stageDuration.WithLabelValues("hook-stage"))); count != 1 || sum < 1.49 || sum > 1.51 {
		t.Errorf("HookStageDuration count=%d sum=%v, want 1/~1.5", count, sum)
	}

	plugin.HookPluginDuration("hook-plugin", 250*time.Millisecond)
	if count, sum := histogramSamples(t, asCollector(t, pluginDuration.WithLabelValues("hook-plugin"))); count != 1 || sum < 0.24 || sum > 0.26 {
		t.Errorf("HookPluginDuration count=%d sum=%v, want 1/~0.25", count, sum)
	}

	plugin.HookPluginFailure("hook-plugin")
	if got := testutil.ToFloat64(pluginFailures.WithLabelValues("hook-plugin")); got != 1 {
		t.Errorf("HookPluginFailure = %v, want 1", got)
	}

	plugin.HookLLMTokens("hook-stage", "hook-model", 123)
	if got := testutil.ToFloat64(llmTokens.WithLabelValues("hook-stage", "hook-model")); got != 123 {
		t.Errorf("HookLLMTokens = %v, want 123", got)
	}

	plugin.HookLLMCache("hook-model", 40, 100)
	if got := testutil.ToFloat64(llmCachedTokens.WithLabelValues("hook-model")); got != 40 {
		t.Errorf("HookLLMCache cached = %v, want 40", got)
	}
	if got := testutil.ToFloat64(llmPromptTokens.WithLabelValues("hook-model")); got != 100 {
		t.Errorf("HookLLMCache prompt = %v, want 100", got)
	}

	plugin.HookFindings("high", "hook-plugin", 2)
	if got := testutil.ToFloat64(findingsTotal.WithLabelValues("high", "hook-plugin")); got != 2 {
		t.Errorf("HookFindings = %v, want 2", got)
	}

	plugin.HookFindingsByRepo("github", "o/hook-repo", "high", 3)
	if got := testutil.ToFloat64(findingsByRepo.WithLabelValues("github", "o/hook-repo", "high")); got != 3 {
		t.Errorf("HookFindingsByRepo = %v, want 3", got)
	}

	plugin.HookTaskDurationByRepo("github", "o/hook-repo", "pr", 12.5)
	if count, sum := histogramSamples(t, asCollector(t, taskDurationByRepo.WithLabelValues("github", "o/hook-repo", "pr"))); count != 1 || sum < 12.49 || sum > 12.51 {
		t.Errorf("HookTaskDurationByRepo count=%d sum=%v, want 1/12.5", count, sum)
	}

	plugin.HookReflectDrop("missing_evidence")
	if got := testutil.ToFloat64(reflectDropped.WithLabelValues("missing_evidence")); got != 1 {
		t.Errorf("HookReflectDrop = %v, want 1", got)
	}

	plugin.HookReflectDegrade("hook-soft")
	if got := testutil.ToFloat64(reflectDegraded.WithLabelValues("hook-soft")); got != 1 {
		t.Errorf("HookReflectDegrade = %v, want 1", got)
	}

	plugin.HookLayerExec("hook-layer")
	if got := testutil.ToFloat64(layerExec.WithLabelValues("hook-layer")); got != 1 {
		t.Errorf("HookLayerExec = %v, want 1", got)
	}

	plugin.HookVerdict("github", "pr", "APPROVE", "R5.1")
	if got := testutil.ToFloat64(verdicts.WithLabelValues("github", "pr", "APPROVE", "R5.1")); got != 1 {
		t.Errorf("HookVerdict = %v, want 1", got)
	}

	plugin.HookFenceNeutralized(2)
	if got := testutil.ToFloat64(fenceNeutralized); got != 2 {
		t.Errorf("HookFenceNeutralized = %v, want 2", got)
	}

	plugin.HookPluginSkipped("hook-budget")
	if got := testutil.ToFloat64(pluginSkipped.WithLabelValues("hook-budget")); got != 1 {
		t.Errorf("HookPluginSkipped = %v, want 1", got)
	}

	plugin.HookCommentRoute("review", "keyword")
	if got := testutil.ToFloat64(commentRoutes.WithLabelValues("review", "keyword")); got != 1 {
		t.Errorf("HookCommentRoute = %v, want 1", got)
	}

	plugin.HookTaskDeduped("hook-event-id")
	if got := testutil.ToFloat64(taskDeduped.WithLabelValues("hook-event-id")); got != 1 {
		t.Errorf("HookTaskDeduped = %v, want 1", got)
	}

	plugin.HookBestOfN("hook-plugin", "full")
	if got := testutil.ToFloat64(bestOfN.WithLabelValues("hook-plugin", "full")); got != 1 {
		t.Errorf("HookBestOfN = %v, want 1", got)
	}

	storage.HookMigrationLeaseLost()
	if got := testutil.ToFloat64(migrationLeaseLost); got != 1 {
		t.Errorf("HookMigrationLeaseLost = %v, want 1", got)
	}
}

func TestIncrHelpersRecordMetrics(t *testing.T) {
	IncrAuthzDenied("hook-route", "write")
	if got := testutil.ToFloat64(authzDenied.WithLabelValues("hook-route", "write")); got != 1 {
		t.Errorf("IncrAuthzDenied = %v, want 1", got)
	}

	IncrEventDropped("hook-outbox")
	if got := testutil.ToFloat64(eventsDropped.WithLabelValues("hook-outbox")); got != 1 {
		t.Errorf("IncrEventDropped = %v, want 1", got)
	}

	IncrCommentRedelivery()
	if got := testutil.ToFloat64(commentRedelivery); got != 1 {
		t.Errorf("IncrCommentRedelivery = %v, want 1", got)
	}

	IncrBudgetExceeded("hook-real")
	if got := testutil.ToFloat64(budgetExceeded.WithLabelValues("hook-real")); got != 1 {
		t.Errorf("IncrBudgetExceeded = %v, want 1", got)
	}

	IncrSideEffectMismatch(3)
	if got := testutil.ToFloat64(sideEffectMismatch); got != 3 {
		t.Errorf("IncrSideEffectMismatch = %v, want 3", got)
	}

	AddSSEConnections(2)
	if got := testutil.ToFloat64(sseConnections); got != 2 {
		t.Errorf("AddSSEConnections(+2) = %v, want 2", got)
	}
	AddSSEConnections(-1)
	if got := testutil.ToFloat64(sseConnections); got != 1 {
		t.Errorf("AddSSEConnections(-1) = %v, want 1", got)
	}

	SetTaskQueueDepth(5, 2)
	if got := testutil.ToFloat64(tasksPending); got != 5 {
		t.Errorf("tasksPending = %v, want 5", got)
	}
	if got := testutil.ToFloat64(tasksRunning); got != 2 {
		t.Errorf("tasksRunning = %v, want 2", got)
	}

	IncrNotifyFailure("hook-channel")
	if got := testutil.ToFloat64(notifyFailures.WithLabelValues("hook-channel")); got != 1 {
		t.Errorf("IncrNotifyFailure = %v, want 1", got)
	}

	IncrWebhookSkipped("hook-draft")
	if got := testutil.ToFloat64(webhookSkipped.WithLabelValues("hook-draft")); got != 1 {
		t.Errorf("IncrWebhookSkipped = %v, want 1", got)
	}

	IncrWebhookRejected("hook-signature")
	if got := testutil.ToFloat64(webhookRejected.WithLabelValues("hook-signature")); got != 1 {
		t.Errorf("IncrWebhookRejected = %v, want 1", got)
	}

	IncrWebhookEnsure("hook-created")
	if got := testutil.ToFloat64(webhookEnsure.WithLabelValues("hook-created")); got != 1 {
		t.Errorf("IncrWebhookEnsure = %v, want 1", got)
	}

	before := LLMFailoverTotal()
	IncrLLMFailover("hook-r3")
	if got := testutil.ToFloat64(llmFailovers.WithLabelValues("hook-r3")); got != 1 {
		t.Errorf("IncrLLMFailover metric = %v, want 1", got)
	}
	if got := LLMFailoverTotal(); got != before+1 {
		t.Errorf("LLMFailoverTotal = %d, want %d（原子单调计数）", got, before+1)
	}

	IncrCtxFit("hook-r2", "trim")
	if got := testutil.ToFloat64(ctxFit.WithLabelValues("hook-r2", "trim")); got != 1 {
		t.Errorf("IncrCtxFit = %v, want 1", got)
	}

	IncrLLMContinued("hook-r3")
	if got := testutil.ToFloat64(llmContinued.WithLabelValues("hook-r3")); got != 1 {
		t.Errorf("IncrLLMContinued = %v, want 1", got)
	}

	IncrToolAnomaly("hook-search")
	if got := testutil.ToFloat64(toolAnomaly.WithLabelValues("hook-search")); got != 1 {
		t.Errorf("IncrToolAnomaly = %v, want 1", got)
	}

	AddCtxBreakdown("hook-r3", "user", 120)
	if got := testutil.ToFloat64(ctxBreakdown.WithLabelValues("hook-r3", "user")); got != 120 {
		t.Errorf("AddCtxBreakdown = %v, want 120", got)
	}

	IncrR2SoftFailure()
	if got := testutil.ToFloat64(r2SoftFailure); got != 1 {
		t.Errorf("IncrR2SoftFailure = %v, want 1", got)
	}

	IncrQAFailure("hook-llm")
	if got := testutil.ToFloat64(qaFailures.WithLabelValues("hook-llm")); got != 1 {
		t.Errorf("IncrQAFailure = %v, want 1", got)
	}

	IncrPollerFailure("hook-list")
	if got := testutil.ToFloat64(pollerFailures.WithLabelValues("hook-list")); got != 1 {
		t.Errorf("IncrPollerFailure = %v, want 1", got)
	}

	IncrCommentBusy()
	if got := testutil.ToFloat64(commentBusy); got != 1 {
		t.Errorf("IncrCommentBusy = %v, want 1", got)
	}

	IncrTestGenVerify(true)
	IncrTestGenVerify(false)
	if got := testutil.ToFloat64(testGenVerify.WithLabelValues("passed")); got != 1 {
		t.Errorf("IncrTestGenVerify(passed) = %v, want 1", got)
	}
	if got := testutil.ToFloat64(testGenVerify.WithLabelValues("failed")); got != 1 {
		t.Errorf("IncrTestGenVerify(failed) = %v, want 1", got)
	}

	IncrFixVerify(true)
	IncrFixVerify(false)
	if got := testutil.ToFloat64(fixVerify.WithLabelValues("passed")); got != 1 {
		t.Errorf("IncrFixVerify(passed) = %v, want 1", got)
	}
	if got := testutil.ToFloat64(fixVerify.WithLabelValues("failed")); got != 1 {
		t.Errorf("IncrFixVerify(failed) = %v, want 1", got)
	}

	IncrFixPush("hook-branch", "hook-cr-created")
	if got := testutil.ToFloat64(fixPush.WithLabelValues("hook-branch", "hook-cr-created")); got != 1 {
		t.Errorf("IncrFixPush = %v, want 1", got)
	}

	IncrSideEffectUnavailable("hook-mode-off")
	if got := testutil.ToFloat64(sideEffectUnavailable.WithLabelValues("hook-mode-off")); got != 1 {
		t.Errorf("IncrSideEffectUnavailable = %v, want 1", got)
	}

	IncrCommentPostFailure("hook-reply")
	if got := testutil.ToFloat64(commentPostFailures.WithLabelValues("hook-reply")); got != 1 {
		t.Errorf("IncrCommentPostFailure = %v, want 1", got)
	}
}
