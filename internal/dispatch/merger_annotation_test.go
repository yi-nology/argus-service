package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
)

func TestMergerAnnotateCrossReview(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf := &plugin.MergedFindings{Findings: []domain.Finding{
		{File: "a.go", Line: 10, Severity: "high", Comment: "空指针风险", By: "claude"},
		{File: "a.go", Line: 12, Severity: "medium", Comment: "边界未检查", By: "kimi"},
		{File: "b.go", Line: 30, Severity: "low", Comment: "命名", By: "claude"}, // kimi 未复现 → 标注
		{File: "c.go", Line: 1, Severity: "low", Comment: "非编队", By: "gpt"},    // 非：编队意见不标注
	}}
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})

	if mf.CrossReview == "" {
		t.Fatal("应有编队注记: " + mf.CrossReview)
	}
	if !strings.Contains(mf.Findings[2].Comment, "交叉审查分歧") {
		t.Fatalf("仅一方报告的意见应标注分歧: %q", mf.Findings[2].Comment)
	}
	if strings.Contains(mf.Findings[0].Comment, "交叉审查分歧") || strings.Contains(mf.Findings[1].Comment, "交叉审查分歧") {
		t.Fatal("双方都覆盖的区域不应标注")
	}
	if strings.Contains(mf.Findings[3].Comment, "交叉审查分歧") {
		t.Fatal("非编队来源不得标注")
	}
	// 幂等：重复调用不叠加
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})
	if strings.Count(mf.Findings[2].Comment, "交叉审查分歧") != 1 {
		t.Fatalf("标注应幂等: %q", mf.Findings[2].Comment)
	}
}

// 深挖修复回归：Merge 阶段多插件命中同题会把 By 拼成 "a,b"——合并条目（专家意见
// 一致的证据）此前进不了 recs，反而被误标「另一专家未复现」，标注方向反了。
func TestMergerAnnotateCrossReview_MergedByCountsAsReplicated(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf := &plugin.MergedFindings{Findings: []domain.Finding{
		// 双专家命中同题被 Merge 合并：By 已拼为 "claude,kimi"
		{File: "a.go", Line: 10, Severity: "high", Comment: "同位置同问题", By: "claude,kimi"},
		// 仅 claude 报的独立问题：附近无其他编队意见 → 应标注
		{File: "b.go", Line: 30, Severity: "low", Comment: "仅一方报告", By: "claude"},
	}}
	m.AnnotateCrossReview(mf, []string{"claude", "kimi"})

	if strings.Contains(mf.Findings[0].Comment, "交叉审查分歧") {
		t.Fatalf("合并条目（双方已复现）不应被标注: %q", mf.Findings[0].Comment)
	}
	if !strings.Contains(mf.Findings[1].Comment, "交叉审查分歧") {
		t.Fatalf("单方报告仍应标注: %q", mf.Findings[1].Comment)
	}
}

// 收尾批修复回归：指纹不含行号——同文件两处不同位置的同模板意见（相差 >2 行）
// 不再被轮内指纹合并吞掉第二条；行号相容（±2 内）仍正常合并。
func TestMerge_SameFingerprintFarLinesKeptSeparate(t *testing.T) {
	m := NewMerger(nil, logx.Nop())
	mf, err := m.Merge(context.Background(), regretTestPR(), "t1", []Outcome{
		{Name: "ocr", Result: &plugin.Result{Findings: []domain.Finding{
			{File: "a.go", Line: 10, Severity: plugin.SeverityMedium, Comment: "error 未检查"},
			{File: "a.go", Line: 200, Severity: plugin.SeverityMedium, Comment: "error 未检查"},
		}}},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(mf.Findings) != 2 {
		t.Fatalf("行号不相容的同模板意见应保留 2 条，got %d: %+v", len(mf.Findings), mf.Findings)
	}
	if mf.Findings[0].Line != 10 || mf.Findings[1].Line != 200 {
		t.Fatalf("两条应各自保留原行号: %+v", mf.Findings)
	}
}
