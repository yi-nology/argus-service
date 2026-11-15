package main

import (
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
