package covergate

import (
	"strings"
	"testing"

	"golang.org/x/tools/cover"
)

func TestCompare(t *testing.T) {
	base := Baseline{Global: 0.60, Packages: map[string]float64{"internal/a": 0.70, "internal/b": 0.60}}
	cur := Baseline{Global: 0.62, Packages: map[string]float64{"internal/a": 0.69, "internal/b": 0.61}}
	bad := compare(base, cur)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "internal/a") {
		t.Fatalf("package drop must fail: %v", bad)
	}

	cur = Baseline{Global: 0.62, Packages: map[string]float64{"internal/a": 0.71, "internal/b": 0.61, "internal/new": 0.65}}
	bad = compare(base, cur)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "新包 internal/new") {
		t.Fatalf("new package <70%% must fail: %v", bad)
	}

	cur = Baseline{Global: 0.62, Packages: map[string]float64{"internal/a": 0.71, "internal/new": 0.75}}
	bad = compare(base, cur)
	if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), "internal/b 不再被覆盖") {
		t.Fatalf("missing baseline package must fail: %v", bad)
	}
}

func TestComparePass(t *testing.T) {
	base := Baseline{Global: 0.61, Packages: map[string]float64{"internal/a": 0.70}}
	cur := Baseline{Global: 0.62, Packages: map[string]float64{"internal/a": 0.71}}
	if bad := compare(base, cur); len(bad) != 0 {
		t.Fatalf("improvement must pass: %v", bad)
	}
}

// 全局 60% 硬地板：baseline 再低也不能让 0.60 以下判绿；0.60 恰好通过。
// 0.59 红 / 0.60 过（brief 指定边界）。
func TestCompareGlobalHardFloor(t *testing.T) {
	base := Baseline{Global: 0.10, Packages: map[string]float64{"internal/a": 0.10}}
	pkgs := map[string]float64{"internal/a": 0.10}

	bad := compare(base, Baseline{Global: 0.59, Packages: pkgs})
	if len(bad) != 1 {
		t.Fatalf("0.59 应只报硬地板一项: %v", bad)
	}
	if !strings.Contains(bad[0], "硬地板") || !strings.Contains(bad[0], "0.590") {
		t.Fatalf("硬地板回归项信息不足: %q", bad[0])
	}

	if bad := compare(base, Baseline{Global: 0.60, Packages: pkgs}); len(bad) != 0 {
		t.Fatalf("0.60 必须通过硬地板: %v", bad)
	}

	// 硬地板与 baseline 回归检查并存：0.60 过地板但低于高 baseline 仍红。
	high := Baseline{Global: 0.70, Packages: map[string]float64{}}
	bad = compare(high, Baseline{Global: 0.60, Packages: map[string]float64{}})
	if len(bad) != 1 || !strings.Contains(bad[0], "基线") {
		t.Fatalf("应只报 baseline 回归: %v", bad)
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
