package main

import (
	"strings"
	"testing"

	"golang.org/x/tools/cover"
)

func TestCompare(t *testing.T) {
	base := Baseline{Global: 0.40, Packages: map[string]float64{"internal/a": 0.70, "internal/b": 0.60}}
	cur := Baseline{Global: 0.41, Packages: map[string]float64{"internal/a": 0.69, "internal/b": 0.61}}
	if bad := compare(base, cur); len(bad) == 0 {
		t.Fatal("package drop must fail")
	}

	cur = Baseline{Global: 0.41, Packages: map[string]float64{"internal/a": 0.71, "internal/b": 0.61, "internal/new": 0.65}}
	if bad := compare(base, cur); len(bad) == 0 {
		t.Fatal("new package <70% must fail")
	}

	cur = Baseline{Global: 0.41, Packages: map[string]float64{"internal/a": 0.71, "internal/new": 0.75}}
	if bad := compare(base, cur); len(bad) == 0 {
		t.Fatal("missing baseline package must fail")
	}
}

func TestComparePass(t *testing.T) {
	base := Baseline{Global: 0.40, Packages: map[string]float64{"internal/a": 0.70}}
	cur := Baseline{Global: 0.42, Packages: map[string]float64{"internal/a": 0.71}}
	if bad := compare(base, cur); len(bad) != 0 {
		t.Fatalf("improvement must pass: %v", bad)
	}
}

func TestMeasure(t *testing.T) {
	profs := []*cover.Profile{
		{FileName: "internal/a/a.go", Blocks: []cover.ProfileBlock{
			{NumStmt: 3, Count: 1}, {NumStmt: 1, Count: 0},
		}},
		{FileName: "internal/b/b.go", Blocks: []cover.ProfileBlock{
			{NumStmt: 4, Count: 1},
		}},
	}
	got := measure(profs)
	if got.Packages["internal/a"] != 0.75 {
		t.Fatalf("a=%v", got.Packages["internal/a"])
	}
	if got.Packages["internal/b"] != 1.0 {
		t.Fatalf("b=%v", got.Packages["internal/b"])
	}
	if got.Global != 7.0/8.0 {
		t.Fatalf("global=%v", got.Global)
	}
}

// -write 的核心承诺：当前值更低也绝不把基线写低（逐包 + 全局）。
func TestMergeBaselinesNeverLowersFloors(t *testing.T) {
	old := Baseline{Global: 0.6608, Packages: map[string]float64{
		"argusctx":  0.5112612612612613,
		"bootstrap": 0.3072916666666667,
		"higher":    0.50,
		"gone":      0.40,
	}}
	cur := Baseline{Global: 0.6699, Packages: map[string]float64{
		"argusctx":  0.5098901098901099,
		"bootstrap": 0.3068920676202861,
		"higher":    0.55,
		"new":       0.80,
	}}
	merged, warns := mergeBaselines(old, cur)

	if merged.Global != cur.Global {
		t.Errorf("全局应取 max: got %v want %v", merged.Global, cur.Global)
	}
	if merged.Packages["argusctx"] != old.Packages["argusctx"] {
		t.Errorf("argusctx 被下调: got %v want %v", merged.Packages["argusctx"], old.Packages["argusctx"])
	}
	if merged.Packages["bootstrap"] != old.Packages["bootstrap"] {
		t.Errorf("bootstrap 被下调: got %v want %v", merged.Packages["bootstrap"], old.Packages["bootstrap"])
	}
	if merged.Packages["higher"] != 0.55 {
		t.Errorf("改善未吸收: got %v want 0.55", merged.Packages["higher"])
	}
	if merged.Packages["gone"] != 0.40 {
		t.Errorf("消失的旧包应保留旧地板: got %v want 0.40", merged.Packages["gone"])
	}
	if merged.Packages["new"] != 0.80 {
		t.Errorf("新包未写入: got %v want 0.80", merged.Packages["new"])
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "gone") {
		t.Errorf("消失的旧包必须告警: %v", warns)
	}
}

func TestMergeBaselinesLowerGlobalKeepsOld(t *testing.T) {
	old := Baseline{Global: 0.66, Packages: map[string]float64{}}
	cur := Baseline{Global: 0.64, Packages: map[string]float64{}}
	merged, _ := mergeBaselines(old, cur)
	if merged.Global != 0.66 {
		t.Fatalf("全局下调被写入: got %v want 0.66", merged.Global)
	}
}

// 写基线仍拒绝基线外的新包 <70%，但不因既有包下调而拒绝（地板由 merge 保住）。
func TestNewPackageViolationsOnlyJudgesNew(t *testing.T) {
	old := Baseline{Packages: map[string]float64{"a": 0.90}}
	cur := Baseline{Packages: map[string]float64{"a": 0.60, "new-bad": 0.65, "new-ok": 0.75}}
	bad := newPackageViolations(old, cur)
	if len(bad) != 1 || !strings.Contains(bad[0], "new-bad") {
		t.Fatalf("只应判新包: %v", bad)
	}
}
