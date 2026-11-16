// qualityops_cover_test.go —— 质量运营数据面补测（#36 kernel 60/70）：
// 批次序列装载（含旧口径探测/坏文件容忍）、环比序列、有效发现率/修复率
// 可证伪指标、环比报告渲染。
package qualityops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadBatchPoints 批次序列：*.json 装载、GeneratedAt 升序、旧口径探测
// （clean_case_fp_rate 缺键 → OldMetric）、坏文件/非 JSON 跳过。
func TestLoadBatchPoints(t *testing.T) {
	dir := t.TempDir()
	// 新口径批（带 clean_case_fp_rate）。
	new1 := `{"generated_at":"2026-01-02","model":"m1","errored":1,
		"overall":{"precision":0.8,"recall":0.7,"f1":0.75,"clean_case_fp_rate":0.1,"unlabeled_count":3}}`
	// 旧口径批（缺 clean_case_fp_rate 键）。
	old1 := `{"generated_at":"2026-01-01","model":"m0","overall":{"precision":0.6,"recall":0.5,"f1":0.55}}`
	// 坏 JSON。
	if err := os.WriteFile(filepath.Join(dir, "b-new.json"), []byte(new1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a-old.json"), []byte(old1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}
	pts, err := LoadBatchPoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 2 {
		t.Fatalf("应装载 2 个批次（坏 JSON/非 JSON 跳过）, got %d: %+v", len(pts), pts)
	}
	if pts[0].Name != "a-old" || pts[1].Name != "b-new" {
		t.Fatalf("应按 GeneratedAt 升序: %+v", pts)
	}
	if !pts[0].OldMetric || pts[1].OldMetric {
		t.Fatalf("口径探测失真: %+v", pts)
	}
	if pts[1].Precision != 0.8 || pts[1].UnlabeledCount != 3 || pts[1].Errored != 1 {
		t.Fatalf("新口径字段装载失真: %+v", pts[1])
	}

	// 空目录。
	pts2, err := LoadBatchPoints(t.TempDir())
	if err != nil || len(pts2) != 0 {
		t.Fatalf("空目录应空序列: %v %v", pts2, err)
	}
}

// TestDeltas 环比序列：n-1 段、跨口径切换线标注。
func TestDeltas(t *testing.T) {
	pts := []BatchPoint{
		{Name: "a", OldMetric: true, Precision: 0.6, Recall: 0.5, F1: 0.55},
		{Name: "b", Precision: 0.8, Recall: 0.7, F1: 0.75, CleanCaseFPRate: 0.1, UnlabeledCount: 3},
		{Name: "c", Precision: 0.9, Recall: 0.6, F1: 0.72, CleanCaseFPRate: 0.2, UnlabeledCount: 1},
	}
	ds := Deltas(pts)
	if len(ds) != 2 {
		t.Fatalf("n-1 段应 2, got %d", len(ds))
	}
	if !ds[0].CrossMetric {
		t.Fatal("旧→新应标跨口径")
	}
	if (ds[0].Precision-0.2) > 1e-9 || (ds[1].F1+0.03) > 1e-9 || ds[1].Unlabeled != -2 {
		t.Fatalf("差值计算失真: %+v", ds)
	}
	if Deltas(nil) != nil {
		t.Fatal("空序列应 nil")
	}
}

// TestEffectiveFindings 有效发现率：分母空、verified/structurally_valid 计数。
func TestEffectiveFindings(t *testing.T) {
	rate, num, den := EffectiveFindings([]string{"verified", "structurally_valid", "refuted", "unverified"})
	if den != 4 || num != 2 || rate != 0.5 {
		t.Fatalf("有效发现率失真: %f %d/%d", rate, num, den)
	}
	if r, n, d := EffectiveFindings(nil); r != 0 || n != 0 || d != 0 {
		t.Fatalf("空分母应零值: %f %d %d", r, n, d)
	}
}

// TestFixRate 修复率：merged/(merged+open)，failed/not_tried 不入分母。
func TestFixRate(t *testing.T) {
	rate, merged, denom := FixRate([]FixOutcome{FixMerged, FixOpen, FixFailed, FixNotTried, FixMerged})
	if merged != 2 || denom != 3 || (rate < 0.66 || rate > 0.67) {
		t.Fatalf("修复率失真: %f %d/%d", rate, merged, denom)
	}
	if r, m, d := FixRate([]FixOutcome{FixFailed}); r != 0 || m != 0 || d != 0 {
		t.Fatalf("全 failed 分母应为 0: %f %d %d", r, m, d)
	}
}

// TestRenderDeltaMarkdown 环比报告渲染：批次表 + 跨口径警示行 + 纪律尾注。
func TestRenderDeltaMarkdown(t *testing.T) {
	pts := []BatchPoint{
		{Name: "a", GeneratedAt: "2026-01-01", OldMetric: true, Precision: 0.6, Recall: 0.5, F1: 0.55},
		{Name: "b", GeneratedAt: "2026-01-02", Precision: 0.8, Recall: 0.7, F1: 0.75, CleanCaseFPRate: 0.1, UnlabeledCount: 3},
	}
	out := RenderDeltaMarkdown(pts, Deltas(pts))
	for _, want := range []string{
		"质量运营环比报告", "| a | 2026-01-01 |", "| b | 2026-01-02 |", "旧", "新",
		"a → b", "跨口径切换线，禁直比", "红绿判定纪律",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("环比报告缺 %q:\n%s", want, out)
		}
	}
}
