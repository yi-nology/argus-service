package archcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --write-baseline 不得把例外上限写高（只允许持平/收缩）。
func TestExceptionCapCannotRaise(t *testing.T) {
	if err := exceptionCapViolation(1, 2); err == nil {
		t.Fatal("旧上限 1、新例外 2 必须拒绝")
	}
	if err := exceptionCapViolation(0, 1); err == nil {
		t.Fatal("旧上限 0、新例外 1 必须拒绝")
	}
	if err := exceptionCapViolation(2, 1); err != nil {
		t.Fatalf("收缩应允许: %v", err)
	}
	if err := exceptionCapViolation(1, 1); err != nil {
		t.Fatalf("持平应允许: %v", err)
	}
	if err := exceptionCapViolation(-1, 5); err != nil {
		t.Fatalf("首次写入（基线缺失/非法）应允许: %v", err)
	}
}

// 检查模式：例外基线缺失必须 fail closed，不得退化成 -1 跳过。
func TestResolveMaxExceptionsFailsClosed(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "arch-exceptions.baseline")
	if _, err := resolveMaxExceptions(-1, missing); err == nil {
		t.Fatal("基线缺失必须报错")
	}
	if err := os.WriteFile(missing, []byte("not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveMaxExceptions(-1, missing); err == nil {
		t.Fatal("基线非法必须报错")
	}
	if err := os.WriteFile(missing, []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveMaxExceptions(-1, missing)
	if err != nil || got != 2 {
		t.Fatalf("应读到基线 2: got=%d err=%v", got, err)
	}
	if got, err := resolveMaxExceptions(5, missing); err != nil || got != 5 {
		t.Fatalf("显式参数应优先: got=%d err=%v", got, err)
	}
}

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
