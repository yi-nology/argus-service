package evalq_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// TestScoreSeverity 分严重度聚合：期望按金标准 must_find 的 severity 归桶、
// 实际上报按实际上报 severity 归桶、TP/FP/FN 分档对账、零分母置 0。
func TestScoreSeverity(t *testing.T) {
	cases := []evalq.Case{
		// 两条 high 期望（一条命中一条漏检）+ 实际上报 2 high 1 low
		mkCase("h1", "go", evalq.LabelStandard, evalq.VerdictRequestChanges,
			evalq.FindingExpect{Kind: "nil-deref", Severity: "high"},
			evalq.FindingExpect{Kind: "missing-tx", Severity: "HIGH"}), // 大小写归一
		// medium 期望漏检，实际另报一条 medium（非常规样例的多报不计 FP）
		mkCase("m1", "go", evalq.LabelStandard, evalq.VerdictComment,
			evalq.FindingExpect{Kind: "error-swallow", Severity: "medium"}),
		// 无 severity 标注的期望归 unspecified 桶
		mkCase("u1", "go", evalq.LabelStandard, evalq.VerdictComment,
			evalq.FindingExpect{Kind: "style-nit"}),
		// clean 样例上报一条 high → FP 计 high 桶
		mkCase("c1", "go", evalq.LabelKnownFP, evalq.VerdictApprove),
	}
	results := []evalq.CaseResult{
		{CaseID: "h1", VerdictHit: true, TPs: []int{0}, FNs: []int{1},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictRequestChanges, Findings: []evalq.Finding{
				{Kind: "nil-deref", Severity: "high"},
				{Kind: "missing-tx", Severity: "high"},
				{Kind: "style-nit", Severity: "low"},
			}}},
		{CaseID: "m1", FNs: []int{0},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictComment, Findings: []evalq.Finding{
				{Kind: "doc-comment", Severity: "medium"},
			}}},
		{CaseID: "u1", FNs: []int{0}, Outcome: evalq.Outcome{Verdict: evalq.VerdictComment}},
		{CaseID: "c1", VerdictHit: false,
			FPs: []evalq.Finding{{Kind: "naming", Severity: "high"}},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictComment, Findings: []evalq.Finding{
				{Kind: "naming", Severity: "high"},
			}}},
	}
	rows := evalq.ScoreSeverity(cases, results)
	bySev := map[string]evalq.SeverityReport{}
	order := make([]string, 0, len(rows))
	for _, r := range rows {
		bySev[r.Severity] = r
		order = append(order, r.Severity)
	}
	// 行序：词表（high/medium/low）在前，未标注垫底
	wantOrder := []string{"high", "medium", "low", "unspecified"}
	if len(order) != len(wantOrder) {
		t.Fatalf("分严重度行 = %v, want %v", order, wantOrder)
	}
	for i, s := range wantOrder {
		if order[i] != s {
			t.Fatalf("分严重度行序 = %v, want %v", order, wantOrder)
		}
	}

	h := bySev["high"]
	if h.Expected != 2 || h.Reported != 3 || h.TP != 1 || h.FN != 1 || h.FP != 1 {
		t.Fatalf("high 桶: %+v, want expected=2 reported=3 tp=1 fn=1 fp=1", h)
	}
	if abs(h.Precision-0.5) > 1e-9 || abs(h.Recall-0.5) > 1e-9 || abs(h.MissRate-0.5) > 1e-9 {
		t.Fatalf("high 桶比率: p=%f r=%f miss=%f, want 全 0.5", h.Precision, h.Recall, h.MissRate)
	}

	m := bySev["medium"]
	if m.Expected != 1 || m.Reported != 1 || m.TP != 0 || m.FN != 1 || m.FP != 0 {
		t.Fatalf("medium 桶: %+v, want expected=1 reported=1 tp=0 fn=1 fp=0", m)
	}
	// 零分母置 0（Precision 分母 TP+FP=0）
	if m.Precision != 0 || m.Recall != 0 || abs(m.MissRate-1) > 1e-9 {
		t.Fatalf("medium 桶比率: p=%f r=%f miss=%f, want 0/0/1", m.Precision, m.Recall, m.MissRate)
	}

	l := bySev["low"]
	if l.Expected != 0 || l.Reported != 1 || l.TP != 0 || l.FP != 0 || l.FN != 0 {
		t.Fatalf("low 桶: %+v, want expected=0 reported=1 全 0 计数", l)
	}
	// 零分母置 0（无期望也无误报）
	if l.Precision != 0 || l.Recall != 0 || l.MissRate != 0 {
		t.Fatalf("low 桶比率应全 0: %+v", l)
	}

	u := bySev["unspecified"]
	if u.Expected != 1 || u.FN != 1 {
		t.Fatalf("unspecified 桶: %+v, want expected=1 fn=1", u)
	}
}

// TestScoreSeverityEmpty 空数据也出词表行（零分母置 0，表形态稳定）。
func TestScoreSeverityEmpty(t *testing.T) {
	rows := evalq.ScoreSeverity(nil, nil)
	got := map[string]bool{}
	for _, r := range rows {
		got[r.Severity] = true
	}
	for _, s := range []string{"high", "medium", "low"} {
		if !got[s] {
			t.Fatalf("空数据应仍出 %s 行: %v", s, got)
		}
	}
	for _, r := range rows {
		if r.Expected != 0 || r.Reported != 0 || r.TP != 0 || r.FP != 0 || r.FN != 0 ||
			r.Precision != 0 || r.Recall != 0 || r.MissRate != 0 {
			t.Fatalf("空数据行应全 0: %+v", r)
		}
	}
}

// TestSeverityReportRender 分严重度聚合双形态：JSON 段 + Markdown 分严重度表。
func TestSeverityReportRender(t *testing.T) {
	cases := []evalq.Case{
		mkCase("h1", "go", evalq.LabelStandard, evalq.VerdictRequestChanges,
			evalq.FindingExpect{Kind: "nil-deref", Severity: "high"}),
	}
	results := []evalq.CaseResult{
		{CaseID: "h1", VerdictHit: true, TPs: []int{0},
			Outcome: evalq.Outcome{Verdict: evalq.VerdictRequestChanges, Findings: []evalq.Finding{
				{Kind: "nil-deref", Severity: "high"},
			}}},
	}
	rep := &evalq.Report{Cases: 1, Overall: evalq.Score(results), Results: results}
	rep.BySeverity = evalq.ScoreSeverity(cases, results)

	var jsonBuf bytes.Buffer
	if err := rep.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonBuf.String(), `"by_severity"`) {
		t.Error("JSON 报告缺 by_severity 段")
	}

	var mdBuf bytes.Buffer
	if err := rep.WriteMarkdown(&mdBuf); err != nil {
		t.Fatal(err)
	}
	md := mdBuf.String()
	for _, want := range []string{"## 分严重度指标", "| high |", "| 期望条数 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺分严重度表 %q", want)
		}
	}
}
