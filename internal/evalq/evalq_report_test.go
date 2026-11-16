package evalq_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/evalq"
)

// TestEvaluateNilReviewer Reviewer 未装配显式报错（接入点契约）。
func TestEvaluateNilReviewer(t *testing.T) {
	if _, err := evalq.Evaluate(context.Background(), []evalq.Case{mkCase("a", "go", evalq.LabelStandard, "approve")}, nil, evalq.Options{}); err == nil {
		t.Fatal("nil Reviewer 应报错")
	}
}

// TestReportRender 报告双形态：JSON 关键段、Markdown 表头与逐案行。
func TestReportRender(t *testing.T) {
	rev := evalq.ReviewerFunc(func(_ context.Context, c evalq.Case) (evalq.Outcome, error) {
		if c.ID == "boom" {
			return evalq.Outcome{}, errors.New("llm timeout")
		}
		return evalq.Outcome{Verdict: evalq.VerdictApprove}, nil
	})
	cases := []evalq.Case{
		mkCase("ok", "go", evalq.LabelKnownFP, evalq.VerdictApprove),
		mkCase("boom", "go", evalq.LabelStandard, evalq.VerdictApprove, evalq.FindingExpect{Kind: "nil-deref"}),
	}
	rep, err := evalq.Evaluate(context.Background(), cases, rev, evalq.Options{Model: "stub", Params: map[string]string{"temperature": "0.2"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Errored != 1 {
		t.Fatalf("errored = %d, want 1", rep.Errored)
	}

	var jsonBuf bytes.Buffer
	if err := rep.WriteJSON(&jsonBuf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"fp_rate"`, `"stability"`, `"by_language"`,
		`"clean_case_fp_rate"`, `"unlabeled_count"`} {
		if !strings.Contains(jsonBuf.String(), want) {
			t.Errorf("JSON 报告缺 %s", want)
		}
	}

	var mdBuf bytes.Buffer
	if err := rep.WriteMarkdown(&mdBuf); err != nil {
		t.Fatal(err)
	}
	md := mdBuf.String()
	for _, want := range []string{"# 基准集质量评测报告", "| Precision（受限标注口径） |",
		"| go |", "## 逐案明细", "llm timeout", "干净样例误报率", "未标注多报"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown 报告缺 %q", want)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
