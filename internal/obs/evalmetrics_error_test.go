package obs

// RefreshEvalResults 的补充边界：坏 JSON / 坏时间戳必须报错且不写 gauge；
// v3.15.0 误报/clean approve 口径要真正落到 gauge 上。

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRefreshEvalResultsMalformedJSON(t *testing.T) {
	path := writeEvalFixture(t, `{not json`)
	err := RefreshEvalResults(path)
	if err == nil || !strings.Contains(err.Error(), "解析评测结果") {
		t.Fatalf("坏 JSON 应返回解析错误, got %v", err)
	}
}

func TestRefreshEvalResultsBadTimestamp(t *testing.T) {
	evalMatchRatio.Reset()
	path := writeEvalFixture(t, `{"generated_at":"18 Sep 2026","model":"m","done":true}`)
	err := RefreshEvalResults(path)
	if err == nil || !strings.Contains(err.Error(), "解析评测时间") {
		t.Fatalf("坏时间戳应返回解析错误, got %v", err)
	}
	// 失败不得留下半写入的序列。
	if got := testutil.CollectAndCount(evalMatchRatio); got != 0 {
		t.Fatalf("坏时间戳写入了 %d 个 match_ratio 序列", got)
	}
}

func TestRefreshEvalResultsFalsePositiveGauges(t *testing.T) {
	evalFalsePositives.Reset()
	evalCleanApprove.Reset()
	path := writeEvalFixture(t, `{
	  "generated_at": "2026-09-18T12:00:00Z",
	  "model": "fp-model", "stage_models": {}, "total": 10, "passed": 8,
	  "pass_rate": 0.8, "done": true,
	  "metrics": {"clean_total": 4, "clean_approve": 3, "false_positives": 7,
	              "boundary_total": 2, "boundary_hits": 1}
	}`)
	if err := RefreshEvalResults(path); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}
	labels := map[string]string{"model": "fp-model", "route": "global"}
	if got := testutil.ToFloat64(evalFalsePositives.With(labels)); got != 7 {
		t.Errorf("false_positives = %v, want 7", got)
	}
	if got := testutil.ToFloat64(evalCleanApprove.With(labels)); got != 3 {
		t.Errorf("clean_approve = %v, want 3", got)
	}
}
