package obs

// Init/导出计数面的行为测试：Init 必须把全部 collector 注册进 metrics.Registry
// 并注入 plugin/storage 观测钩子；每个 Incr* 助手必须落到正确指标与标签上。
//
// 计数断言一律用「读前值→动作→增量」：Prometheus collector 是进程级全局，
// go test -count=N 会重跑同一进程，绝对 ==1 会随累计值漂移。

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
// 它只保证「注册幂等」；指标值仍是进程级累计，所以断言必须用增量而非绝对值。
var obsInitOnce sync.Once

func ensureObsInitialized() { obsInitOnce.Do(Init) }

// deltaCounter 读前值 → 执行 action → 返回 counter/gauge 增量。
func deltaCounter(t *testing.T, c prometheus.Collector, action func()) float64 {
	t.Helper()
	before := testutil.ToFloat64(c)
	action()
	return testutil.ToFloat64(c) - before
}

// deltaHistogram 读前后样本数与样本和，返回本次 action 的增量。
func deltaHistogram(t *testing.T, c prometheus.Collector, action func()) (uint64, float64) {
	t.Helper()
	beforeCount, beforeSum := histogramSamples(t, c)
	action()
	afterCount, afterSum := histogramSamples(t, c)
	return afterCount - beforeCount, afterSum - beforeSum
}

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
		{"HookReplyThrottled", plugin.HookReplyThrottled != nil},
		{"HookFindingsSilenced", plugin.HookFindingsSilenced != nil},
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

// TestInitHooksRecordMetrics 逐个驱动钩子并对账增量/标签——钩子存在不代表
// 埋点接对了线（闭包里写错 collector 会在这里被抓到）。
func TestInitHooksRecordMetrics(t *testing.T) {
	ensureObsInitialized()

	if got := deltaCounter(t, tasksTotal.WithLabelValues("hookplat", "kind1", "done", "hooktenant"), func() {
		plugin.HookTaskTotal("hooktenant", "hookplat", "kind1", "done")
	}); got != 1 {
		t.Errorf("HookTaskTotal delta = %v, want 1", got)
	}

	if count, sum := deltaHistogram(t, asCollector(t, stageDuration.WithLabelValues("hook-stage", "hooktenant")), func() {
		plugin.HookStageDuration("hooktenant", "hook-stage", 1500*time.Millisecond)
	}); count != 1 || sum < 1.49 || sum > 1.51 {
		t.Errorf("HookStageDuration count=%d sum=%v, want 1/~1.5", count, sum)
	}

	if count, sum := deltaHistogram(t, asCollector(t, pluginDuration.WithLabelValues("hook-plugin", "hooktenant")), func() {
		plugin.HookPluginDuration("hooktenant", "hook-plugin", 250*time.Millisecond)
	}); count != 1 || sum < 0.24 || sum > 0.26 {
		t.Errorf("HookPluginDuration count=%d sum=%v, want 1/~0.25", count, sum)
	}

	if got := deltaCounter(t, pluginFailures.WithLabelValues("hook-plugin", "hooktenant"), func() {
		plugin.HookPluginFailure("hooktenant", "hook-plugin")
	}); got != 1 {
		t.Errorf("HookPluginFailure delta = %v, want 1", got)
	}

	if got := deltaCounter(t, llmTokens.WithLabelValues("hook-stage", "hook-model", "hooktenant"), func() {
		plugin.HookLLMTokens("hooktenant", "hook-stage", "hook-model", 123)
	}); got != 123 {
		t.Errorf("HookLLMTokens delta = %v, want 123", got)
	}

	cachedBefore := testutil.ToFloat64(llmCachedTokens.WithLabelValues("hook-model", "hooktenant"))
	promptBefore := testutil.ToFloat64(llmPromptTokens.WithLabelValues("hook-model", "hooktenant"))
	plugin.HookLLMCache("hooktenant", "hook-model", 40, 100)
	if got := testutil.ToFloat64(llmCachedTokens.WithLabelValues("hook-model", "hooktenant")) - cachedBefore; got != 40 {
		t.Errorf("HookLLMCache cached delta = %v, want 40", got)
	}
	if got := testutil.ToFloat64(llmPromptTokens.WithLabelValues("hook-model", "hooktenant")) - promptBefore; got != 100 {
		t.Errorf("HookLLMCache prompt delta = %v, want 100", got)
	}

	if got := deltaCounter(t, findingsTotal.WithLabelValues("high", "hook-plugin", "hooktenant"), func() {
		plugin.HookFindings("hooktenant", "high", "hook-plugin", 2)
	}); got != 2 {
		t.Errorf("HookFindings delta = %v, want 2", got)
	}

	if got := deltaCounter(t, findingsByRepo.WithLabelValues("github", "o/hook-repo", "high", "hooktenant"), func() {
		plugin.HookFindingsByRepo("hooktenant", "github", "o/hook-repo", "high", 3)
	}); got != 3 {
		t.Errorf("HookFindingsByRepo delta = %v, want 3", got)
	}

	if got := deltaCounter(t, replyThrottled.WithLabelValues("github", "o/hook-repo", "hooktenant"), func() {
		plugin.HookReplyThrottled("hooktenant", "github", "o/hook-repo")
	}); got != 1 {
		t.Errorf("HookReplyThrottled delta = %v, want 1", got)
	}

	if got := deltaCounter(t, findingsSilenced.WithLabelValues("github", "o/hook-repo", "floor", "hooktenant"), func() {
		plugin.HookFindingsSilenced("hooktenant", "github", "o/hook-repo", "floor", 4)
	}); got != 4 {
		t.Errorf("HookFindingsSilenced delta = %v, want 4", got)
	}

	if count, sum := deltaHistogram(t, asCollector(t, taskDurationByRepo.WithLabelValues("github", "o/hook-repo", "pr", "hooktenant")), func() {
		plugin.HookTaskDurationByRepo("hooktenant", "github", "o/hook-repo", "pr", 12.5)
	}); count != 1 || sum < 12.49 || sum > 12.51 {
		t.Errorf("HookTaskDurationByRepo count=%d sum=%v, want 1/12.5", count, sum)
	}

	if got := deltaCounter(t, reflectDropped.WithLabelValues("missing_evidence", "hooktenant"), func() {
		plugin.HookReflectDrop("hooktenant", "missing_evidence")
	}); got != 1 {
		t.Errorf("HookReflectDrop delta = %v, want 1", got)
	}

	if got := deltaCounter(t, reflectDegraded.WithLabelValues("hook-soft", "hooktenant"), func() {
		plugin.HookReflectDegrade("hooktenant", "hook-soft")
	}); got != 1 {
		t.Errorf("HookReflectDegrade delta = %v, want 1", got)
	}

	if got := deltaCounter(t, layerExec.WithLabelValues("hook-layer", "hooktenant"), func() {
		plugin.HookLayerExec("hooktenant", "hook-layer")
	}); got != 1 {
		t.Errorf("HookLayerExec delta = %v, want 1", got)
	}

	if got := deltaCounter(t, verdicts.WithLabelValues("github", "pr", "APPROVE", "R5.1", "hooktenant"), func() {
		plugin.HookVerdict("hooktenant", "github", "pr", "APPROVE", "R5.1")
	}); got != 1 {
		t.Errorf("HookVerdict delta = %v, want 1", got)
	}

	if got := deltaCounter(t, fenceNeutralized.WithLabelValues("hooktenant"), func() {
		plugin.HookFenceNeutralized("hooktenant", 2)
	}); got != 2 {
		t.Errorf("HookFenceNeutralized delta = %v, want 2", got)
	}

	if got := deltaCounter(t, pluginSkipped.WithLabelValues("hook-budget", "hooktenant"), func() {
		plugin.HookPluginSkipped("hooktenant", "hook-budget")
	}); got != 1 {
		t.Errorf("HookPluginSkipped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, commentRoutes.WithLabelValues("review", "keyword", "hooktenant"), func() {
		plugin.HookCommentRoute("hooktenant", "review", "keyword")
	}); got != 1 {
		t.Errorf("HookCommentRoute delta = %v, want 1", got)
	}

	if got := deltaCounter(t, taskDeduped.WithLabelValues("hook-event-id", "hooktenant"), func() {
		plugin.HookTaskDeduped("hooktenant", "hook-event-id")
	}); got != 1 {
		t.Errorf("HookTaskDeduped delta = %v, want 1", got)
	}

	if got := deltaCounter(t, bestOfN.WithLabelValues("hook-plugin", "full", "hooktenant"), func() {
		plugin.HookBestOfN("hooktenant", "hook-plugin", "full")
	}); got != 1 {
		t.Errorf("HookBestOfN delta = %v, want 1", got)
	}

	if got := deltaCounter(t, migrationLeaseLost, func() {
		storage.HookMigrationLeaseLost()
	}); got != 1 {
		t.Errorf("HookMigrationLeaseLost delta = %v, want 1", got)
	}
}
