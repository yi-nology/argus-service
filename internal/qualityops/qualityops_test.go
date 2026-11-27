package qualityops

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("写测试夹具 %s: %v", name, err)
	}
}

func approx(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
}

// TestLoadBatchPoints 目录读取：新旧口径标注、按 GeneratedAt 升序、坏 JSON/
// 目录条目跳过不阻断、非 .json 忽略、读不存在的目录返回空序列。
func TestLoadBatchPoints(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "zero.json", `{
		"generated_at":"2025-11-01T00:00:00Z","model":"m0","errored":0,
		"overall":{"precision":0.5,"recall":0.4,"f1":0.45,
			"clean_case_fp_rate":0,"unlabeled_count":0}}`)
	writeFile(t, dir, "null-metric.json", `{
		"generated_at":"2025-12-01T00:00:00Z","model":"m0",
		"overall":{"precision":0.6,"recall":0.5,"f1":0.55,
			"clean_case_fp_rate":null,"unlabeled_count":0}}`)
	writeFile(t, dir, "old.json", `{
		"generated_at":"2026-01-01T00:00:00Z","model":"m1","errored":1,
		"overall":{"precision":0.7,"recall":0.6,"f1":0.65}}`)
	writeFile(t, dir, "new.json", `{
		"generated_at":"2026-02-01T00:00:00Z","model":"m2","errored":2,
		"overall":{"precision":0.9,"recall":0.8,"f1":0.85,
			"clean_case_fp_rate":0.05,"unlabeled_count":3}}`)
	writeFile(t, dir, "broken.json", `{not json`)
	writeFile(t, dir, "notes.txt", `ignored`)
	// 打桩批次（model=stub）必须被读侧过滤：即使 GeneratedAt 最新也不入序列
	//（分名纪律读侧——否则 make test 的 stub 跑批会顶掉真实基线进门禁环比）。
	writeFile(t, dir, "baseline-stub.json", `{
		"generated_at":"2026-03-01T00:00:00Z","model":"stub","errored":0,
		"overall":{"precision":0.1,"recall":0.1,"f1":0.1,
			"clean_case_fp_rate":0.9,"unlabeled_count":0}}`)
	if err := os.Mkdir(filepath.Join(dir, "dir.json"), 0o755); err != nil {
		t.Fatalf("建目录夹具: %v", err)
	}

	pts, err := LoadBatchPoints(dir)
	if err != nil {
		t.Fatalf("LoadBatchPoints: %v", err)
	}
	var names []string
	for _, p := range pts {
		names = append(names, p.Name)
	}
	if got, want := strings.Join(names, ","), "zero,null-metric,old,new"; got != want {
		t.Fatalf("批次顺序 = %q, want %q", got, want)
	}

	// 新口径指标为 0 仍算新口径（缺键才是旧口径）。
	zero := pts[0]
	if zero.OldMetric || zero.CleanCaseFPRate != 0 || zero.UnlabeledCount != 0 || zero.Model != "m0" {
		t.Fatalf("zero 批次解析不符: %+v", zero)
	}
	// 显式 null = 缺键，按旧口径处理；不应 panic。
	if !pts[1].OldMetric {
		t.Fatalf("clean_case_fp_rate=null 应按旧口径: %+v", pts[1])
	}
	if old := pts[2]; !old.OldMetric || old.Precision != 0.7 || old.Recall != 0.6 || old.F1 != 0.65 || old.Errored != 1 {
		t.Fatalf("旧口径批次解析不符: %+v", old)
	}
	if newPt := pts[3]; newPt.OldMetric || newPt.CleanCaseFPRate != 0.05 ||
		newPt.UnlabeledCount != 3 || newPt.Errored != 2 || newPt.GeneratedAt != "2026-02-01T00:00:00Z" {
		t.Fatalf("新口径批次解析不符: %+v", newPt)
	}

	if pts, err := LoadBatchPoints(filepath.Join(dir, "absent")); err != nil || len(pts) != 0 {
		t.Fatalf("不存在目录应返回空序列: pts=%v err=%v", pts, err)
	}
	if _, err := LoadBatchPoints("["); err == nil {
		t.Fatal("非法 glob 模式应报错")
	}
}

func TestLoadBatchPointsMissingUnlabeledCount(t *testing.T) {
	dir := t.TempDir()
	// 只有 clean_case_fp_rate、没有 unlabeled_count：率不可解释，按旧口径
	// 处理；此前直接解引用 *UnlabeledCount 会 panic。
	writeFile(t, dir, "half.json", `{"generated_at":"2025-11-02T00:00:00Z","model":"m","overall":{"precision":0.5,"recall":0.5,"f1":0.5,"clean_case_fp_rate":0.1}}`)
	pts, err := LoadBatchPoints(dir)
	if err != nil {
		t.Fatalf("LoadBatchPoints: %v", err)
	}
	if len(pts) != 1 {
		t.Fatalf("pts=%d, want 1", len(pts))
	}
	if !pts[0].OldMetric {
		t.Fatal("缺 unlabeled_count 应标 OldMetric（不可比），不得当新口径")
	}
}

// TestDeltas 相邻环比：差值方向（后−前）、段数 n-1、跨口径切换线标记。
func TestDeltas(t *testing.T) {
	pts := []BatchPoint{
		{Name: "a", Precision: 0.70, Recall: 0.60, F1: 0.65, CleanCaseFPRate: 0.10, UnlabeledCount: 5},
		{Name: "b", Precision: 0.80, Recall: 0.55, F1: 0.70, CleanCaseFPRate: 0.08, UnlabeledCount: 7},
		{Name: "c", Precision: 0.75, Recall: 0.65, F1: 0.72, CleanCaseFPRate: 0.09, UnlabeledCount: 6, OldMetric: true},
	}
	ds := Deltas(pts)
	if len(ds) != 2 {
		t.Fatalf("环比段数 = %d, want 2", len(ds))
	}

	d := ds[0]
	if d.From != "a" || d.To != "b" {
		t.Fatalf("From/To = %s/%s", d.From, d.To)
	}
	if d.CrossMetric {
		t.Fatal("a→b 同口径不应标跨线")
	}
	approx(t, "ΔP(a→b)", d.Precision, 0.10)
	approx(t, "ΔR(a→b)", d.Recall, -0.05)
	approx(t, "ΔF1(a→b)", d.F1, 0.05)
	approx(t, "ΔCleanCaseFP(a→b)", d.CleanCaseFPRate, -0.02)
	if d.Unlabeled != 2 {
		t.Fatalf("ΔUnlabeled(a→b) = %d, want 2", d.Unlabeled)
	}

	d = ds[1]
	if d.From != "b" || d.To != "c" || !d.CrossMetric {
		t.Fatalf("b→c 应标跨口径线: %+v", d)
	}
	approx(t, "ΔP(b→c)", d.Precision, -0.05)
	approx(t, "ΔR(b→c)", d.Recall, 0.10)
	approx(t, "ΔF1(b→c)", d.F1, 0.02)
	approx(t, "ΔCleanCaseFP(b→c)", d.CleanCaseFPRate, 0.01)
	if d.Unlabeled != -1 {
		t.Fatalf("ΔUnlabeled(b→c) = %d, want -1", d.Unlabeled)
	}

	if got := Deltas(nil); len(got) != 0 {
		t.Fatalf("空序列环比段 = %d", len(got))
	}
	if got := Deltas([]BatchPoint{{Name: "only"}}); len(got) != 0 {
		t.Fatalf("单批次不应产生环比段: %v", got)
	}
}

// TestEffectiveFindings 有效发现率 = (verified + structurally_valid) / 总数；
// 空集除零保护、大小写/空白严格匹配、未知状态不进分子但进分母。
func TestEffectiveFindings(t *testing.T) {
	cases := []struct {
		name     string
		statuses []string
		rate     float64
		num, den int
	}{
		{"nil", nil, 0, 0, 0},
		{"empty", []string{}, 0, 0, 0},
		{"verified_only", []string{"verified"}, 1, 1, 1},
		{"structurally_valid_only", []string{"structurally_valid"}, 1, 1, 1},
		{"mixed", []string{"verified", "rejected", "structurally_valid", "uncertain"}, 0.5, 2, 4},
		{"none_valid", []string{"rejected"}, 0, 0, 1},
		{"case_sensitive", []string{"VERIFIED"}, 0, 0, 1},
		{"no_trim", []string{"verified "}, 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, num, den := EffectiveFindings(tc.statuses)
			approx(t, "rate", rate, tc.rate)
			if num != tc.num || den != tc.den {
				t.Fatalf("num/den = %d/%d, want %d/%d", num, den, tc.num, tc.den)
			}
		})
	}
}

// TestFixRate 修复率 = merged/(merged+open)；not_tried/failed 不入分母。
func TestFixRate(t *testing.T) {
	cases := []struct {
		name          string
		outcomes      []FixOutcome
		rate          float64
		merged, denom int
	}{
		{"nil", nil, 0, 0, 0},
		{"all_merged", []FixOutcome{FixMerged, FixMerged}, 1, 2, 2},
		{"half", []FixOutcome{FixMerged, FixOpen}, 0.5, 1, 2},
		{"mixed", []FixOutcome{FixMerged, FixOpen, FixMerged, FixNotTried, FixNotTried, FixFailed}, 2.0 / 3.0, 2, 3},
		{"not_tried_only", []FixOutcome{FixNotTried, FixNotTried}, 0, 0, 0},
		{"failed_only", []FixOutcome{FixFailed}, 0, 0, 0},
		{"open_only", []FixOutcome{FixOpen}, 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate, merged, denom := FixRate(tc.outcomes)
			approx(t, "rate", rate, tc.rate)
			if merged != tc.merged || denom != tc.denom {
				t.Fatalf("merged/denom = %d/%d, want %d/%d", merged, denom, tc.merged, tc.denom)
			}
		})
	}
}

// TestRenderDeltaMarkdown 报告结构：批次序列行（旧/新口径标注）、带符号环比行、
// 跨口径警示；空输入仍渲染表头与 0 计数。
func TestRenderDeltaMarkdown(t *testing.T) {
	pts := []BatchPoint{
		{Name: "old", GeneratedAt: "2026-01-01T00:00:00Z", Precision: 0.7, Recall: 0.6, F1: 0.65, OldMetric: true},
		{Name: "new", GeneratedAt: "2026-02-01T00:00:00Z", Precision: 0.8, Recall: 0.55, F1: 0.7, CleanCaseFPRate: 0.05, UnlabeledCount: 3},
	}
	ds := Deltas(pts)
	md := RenderDeltaMarkdown(pts, ds)

	for _, want := range []string{
		"# 质量运营环比报告（Phase 4）",
		"- 批次数：2（环比段 1）",
		"| old | 2026-01-01T00:00:00Z | 0.700 | 0.600 | 0.650 | 0.000 | 0 | 旧 |",
		"| new | 2026-02-01T00:00:00Z | 0.800 | 0.550 | 0.700 | 0.050 | 3 | 新 |",
		"| old → new | +0.100 | -0.050 | +0.050 | +0.050 | +3 | **跨口径切换线，禁直比** |",
		"红绿判定纪律",
		"docs/ops/quality-ops.md",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("报告缺少 %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "| — |") {
		t.Fatalf("跨口径行不应出现「—」\n%s", md)
	}

	// 同口径环比行用「—」占位口径线。
	same := []BatchPoint{
		{Name: "a", Precision: 0.7, Recall: 0.6, F1: 0.65},
		{Name: "b", Precision: 0.8, Recall: 0.55, F1: 0.7},
	}
	md = RenderDeltaMarkdown(same, Deltas(same))
	if !strings.Contains(md, "| a → b |") || !strings.Contains(md, "| — |") {
		t.Fatalf("同口径行应带「—」口径线\n%s", md)
	}

	empty := RenderDeltaMarkdown(nil, nil)
	if !strings.Contains(empty, "- 批次数：0（环比段 0）") || strings.Contains(empty, "| a → b |") {
		t.Fatalf("空输入渲染不符:\n%s", empty)
	}
}
