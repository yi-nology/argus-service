package main

import (
	"reflect"
	"testing"
)

func TestParseThresholdAggregatedBlock(t *testing.T) {
	raw := []string{
		"超长文件（阈值 400 行）2 个:\n" +
			"    internal/impactview/html.go (1554 行 > 400)\n" +
			"    internal/storage/store.go (450 行 > 400)\n",
		"god object（阈值 25 个实质方法）1 个:\n" +
			"    internal/runner.Runner (62 个方法 = 2 转发壳 + 60 实质，实质 60 > 25)\n",
	}
	got := parseThreshold(raw)
	want := map[string]int{
		"internal/impactview/html.go": 1554,
		"internal/storage/store.go":   450,
		"internal/runner.Runner":      62,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestRegressions(t *testing.T) {
	base := map[string]int{"a.go": 500, "B": 30}
	cur := map[string]int{"a.go": 500, "B": 30}
	if got := regressions(cur, base); len(got) != 0 {
		t.Fatalf("same should pass: %v", got)
	}
	cur["a.go"] = 480 // 拆分后缩小：允许
	if got := regressions(cur, base); len(got) != 0 {
		t.Fatalf("shrink should pass: %v", got)
	}
	cur["a.go"] = 560 // 同一文件增长：红
	if got := regressions(cur, base); len(got) != 1 {
		t.Fatalf("growth must fail: %v", got)
	}
	cur["a.go"] = 480
	cur["c.go"] = 410 // 新超限文件：红
	if got := regressions(cur, base); len(got) != 1 {
		t.Fatalf("new finding must fail: %v", got)
	}
}

func TestParseThresholdSkipsHeaderAndNoise(t *testing.T) {
	raw := []string{"超长文件（阈值 400 行）0 个:", "not a finding", "  internal/x.go (401 行 > 400)"}
	got := parseThreshold(raw)
	if len(got) != 1 || got["internal/x.go"] != 401 {
		t.Fatalf("got %#v", got)
	}
}
