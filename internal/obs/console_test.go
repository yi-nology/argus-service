package obs

// 控制台数据面测试：白名单是「控制台能看到什么」的单源判定，ConsoleMetrics
// 只能透出白名单族、标签值去空、值取 counter/gauge 当前值。

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/yi-nology/argus/internal/tenant"
)

func TestConsoleMetricEnabledWhitelist(t *testing.T) {
	enabled := []string{
		"argus_tasks_total",
		"argus_budget_exceeded_total",
		"argus_llm_failovers_total",
		"argus_plugin_failures_total",
		"argus_qa_failures_total",
		"argus_fix_verify_total",
		"argus_fix_push_total",
		"argus_testgen_verify_total",
		"argus_comment_post_failures_total",
	}
	for _, name := range enabled {
		if !ConsoleMetricEnabled(name) {
			t.Errorf("白名单漏了 %q", name)
		}
	}
	// 高基数/调试指标不得透出到控制台（/metrics 才是全量面）。
	disabled := []string{
		"argus_stage_duration_seconds",
		"argus_llm_tokens_total",
		"argus_findings_by_repo_total",
		"argus_reply_throttled_total",   // repo 维度高基数：/metrics 面，不进控制台
		"argus_findings_silenced_total", // 同上
		"argus_ctx_breakdown_runes_total",
		"",
	}
	for _, name := range disabled {
		if ConsoleMetricEnabled(name) {
			t.Errorf("非运营指标 %q 不应在白名单", name)
		}
	}
}

func TestConsoleMetricsSnapshotOnlyWhitelistedFamilies(t *testing.T) {
	ensureObsInitialized()

	// 造一个白名单样本（含空标签值，验证 labelMap 去空）与一个非白名单样本。
	// 前值读取：包级计数器跨 -count=N 累计，快照断言用增量。
	taskBefore := testutil.ToFloat64(tasksTotal.WithLabelValues("", "console-kind", "done", "console-tenant"))
	postBefore := testutil.ToFloat64(commentPostFailures.WithLabelValues("console-reply", "console-tenant"))
	tasksTotal.WithLabelValues("", "console-kind", "done", "console-tenant").Inc()
	stageDuration.WithLabelValues("console-nonwhitelist", "console-tenant").Observe(0.5)
	IncrCommentPostFailure(tenant.NewContext(context.Background(), "console-tenant"), "console-reply")

	snapshot := ConsoleMetrics()
	if len(snapshot) == 0 {
		t.Fatal("ConsoleMetrics 为空")
	}

	var taskEntry *ConsoleMetric
	for i := range snapshot {
		m := snapshot[i]
		if !ConsoleMetricEnabled(m.Name) {
			t.Fatalf("控制台透出了白名单外指标 %q", m.Name)
		}
		if m.Name == "argus_tasks_total" && m.Labels["kind"] == "console-kind" && m.Labels["status"] == "done" && m.Labels["tenant"] == "console-tenant" {
			taskEntry = &snapshot[i]
		}
	}
	if taskEntry == nil {
		t.Fatal("快照缺少 argus_tasks_total{kind=console-kind,status=done}")
	}
	// 空标签值不入 map（渲染层不应出现 platform="" 分组）。
	if _, ok := taskEntry.Labels["platform"]; ok {
		t.Errorf("空标签值 platform=\"\" 不应透出: %v", taskEntry.Labels)
	}
	if taskEntry.Value != taskBefore+1 {
		t.Errorf("tasks_total value = %v, want %v（本测试 +1）", taskEntry.Value, taskBefore+1)
	}

	foundPostFailure := false
	for _, m := range snapshot {
		if m.Name == "argus_comment_post_failures_total" && m.Labels["kind"] == "console-reply" {
			foundPostFailure = true
			if m.Value != postBefore+1 {
				t.Errorf("comment_post_failures value = %v, want %v（本测试 +1）", m.Value, postBefore+1)
			}
		}
	}
	if !foundPostFailure {
		t.Fatal("快照缺少 argus_comment_post_failures_total{kind=console-reply}")
	}

	for _, m := range snapshot {
		if m.Name == "argus_stage_duration_seconds" {
			t.Fatal("histogram 非白名单指标不得出现在控制台快照")
		}
	}
}

func TestLabelMapSkipsEmptyAndInternalLabels(t *testing.T) {
	empty := ""
	val := "v"
	got := labelMap(&dto.Metric{Label: []*dto.LabelPair{
		{Name: strPtr("__name__"), Value: &val},
		{Name: strPtr("blank"), Value: &empty},
		{Name: strPtr("keep"), Value: strPtr("x")},
	}})
	if len(got) != 1 || got["keep"] != "x" {
		t.Fatalf("labelMap = %v, want 仅 keep=x", got)
	}

	if got := labelMap(&dto.Metric{}); got != nil {
		t.Fatalf("无标签应返回 nil, got %v", got)
	}
	if got := labelMap(&dto.Metric{Label: []*dto.LabelPair{{Name: strPtr("blank"), Value: &empty}}}); got != nil {
		t.Fatalf("全为空值应返回 nil, got %v", got)
	}
}

func TestCounterValueKinds(t *testing.T) {
	sum := 4.5
	counter := 2.0
	gauge := 3.0
	cases := []struct {
		name string
		m    *dto.Metric
		want float64
	}{
		{"counter", &dto.Metric{Counter: &dto.Counter{Value: &counter}}, 2},
		{"gauge", &dto.Metric{Gauge: &dto.Gauge{Value: &gauge}}, 3},
		{"histogram", &dto.Metric{Histogram: &dto.Histogram{SampleSum: &sum}}, 4.5},
		{"summary", &dto.Metric{Summary: &dto.Summary{SampleSum: &sum}}, 4.5},
		{"none", &dto.Metric{}, 0},
	}
	for _, c := range cases {
		if got := counterValue(c.m); got != c.want {
			t.Errorf("%s: counterValue = %v, want %v", c.name, got, c.want)
		}
	}
}

func strPtr(s string) *string { return &s }
