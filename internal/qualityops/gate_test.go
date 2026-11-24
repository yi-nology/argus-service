// gate_test.go —— 同口径质量门禁（v8.5.0）：阈值判定、边界、跨口径跳过。
package qualityops

import (
	"strings"
	"testing"
)

func TestGatePass(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 0.90, CleanCaseFPRate: 0.10, UnlabeledCount: 5}
	cur := BatchPoint{Name: "b", F1: 0.88, CleanCaseFPRate: 0.12, UnlabeledCount: 10}
	res := Gate(prev, cur, DefaultGateThresholds())
	if !res.OK || res.Skipped || len(res.Reasons) != 0 {
		t.Fatalf("阈值内应通过: %+v", res)
	}
}

func TestGateF1Drop(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 0.90}
	cur := BatchPoint{Name: "b", F1: 0.84}
	res := Gate(prev, cur, DefaultGateThresholds())
	if res.OK || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "F1 下跌") {
		t.Fatalf("F1 跌 0.06 应拒: %+v", res)
	}
	// 恰好等于阈值不算突破（严格大于）
	cur.F1 = 0.85
	if res := Gate(prev, cur, DefaultGateThresholds()); !res.OK {
		t.Fatalf("恰等于阈值应通过: %+v", res)
	}
}

func TestGateFPRise(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 1, CleanCaseFPRate: 0.10}
	cur := BatchPoint{Name: "b", F1: 1, CleanCaseFPRate: 0.16}
	res := Gate(prev, cur, DefaultGateThresholds())
	if res.OK || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "干净样例误报率") {
		t.Fatalf("误报率涨 0.06 应拒: %+v", res)
	}
}

func TestGateUnlabeledRise(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 1, UnlabeledCount: 3}
	cur := BatchPoint{Name: "b", F1: 1, UnlabeledCount: 14}
	res := Gate(prev, cur, DefaultGateThresholds())
	if res.OK || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "Unlabeled 增加") {
		t.Fatalf("Unlabeled 增 11 应拒: %+v", res)
	}
}

func TestGateMultipleReasons(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 0.90, CleanCaseFPRate: 0.10, UnlabeledCount: 1}
	cur := BatchPoint{Name: "b", F1: 0.70, CleanCaseFPRate: 0.30, UnlabeledCount: 20}
	res := Gate(prev, cur, DefaultGateThresholds())
	if res.OK || len(res.Reasons) != 3 {
		t.Fatalf("三项全突破应 3 条原因: %+v", res)
	}
}

func TestGateCrossMetricSkipped(t *testing.T) {
	prev := BatchPoint{Name: "old", OldMetric: true, F1: 1}
	cur := BatchPoint{Name: "new", F1: 0.1}
	res := Gate(prev, cur, DefaultGateThresholds())
	if !res.OK || !res.Skipped || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "跨口径") {
		t.Fatalf("跨口径应跳过: %+v", res)
	}
}

func TestGateZeroThresholdsDisable(t *testing.T) {
	prev := BatchPoint{Name: "a", F1: 0.90, CleanCaseFPRate: 0, UnlabeledCount: 0}
	cur := BatchPoint{Name: "b", F1: 0, CleanCaseFPRate: 1, UnlabeledCount: 999}
	if res := Gate(prev, cur, GateThresholds{}); !res.OK || len(res.Reasons) != 0 {
		t.Fatalf("全零阈值应关闭判定: %+v", res)
	}
}
