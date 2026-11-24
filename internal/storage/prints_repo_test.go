// prints_repo_test.go —— 意见治理台读面（v8.5.0）：跨 PR 列表 + 状态过滤 +
// limit + 三计数汇总。
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

func TestListPrintsByRepoAndSummary(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now()

	mk := func(number, fp string, f domain.Finding) {
		t.Helper()
		if _, err := s.UpsertPrint(ctx, "gitea", "acme", "app", number, fp, f, "t-"+number, at); err != nil {
			t.Fatal(err)
		}
	}
	fA := domain.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "x", By: "correctness"}
	fB := domain.Finding{File: "b.go", Line: 9, Severity: "low", Comment: "y", By: "ocr"}
	mk("7", "fp0aaaaaaaaaaaa", fA)
	mk("8", "fp0bbbbbbbbbbbb", fB)
	// 抑制其中一条（跨 PR：抑制只作用于指定 PR 的指纹）
	if _, err := s.SuppressPrint(ctx, "gitea", "acme", "app", "7", "fp0aaaaaaaaaaaa", fA, at); err != nil {
		t.Fatal(err)
	}

	// 全部：2 条
	rows, err := s.ListPrintsByRepo(ctx, "gitea", "acme", "app", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("全量应 2 条, got %d", len(rows))
	}
	// 状态过滤
	open, _ := s.ListPrintsByRepo(ctx, "gitea", "acme", "app", PrintOpen, 100)
	if len(open) != 1 || open[0].Print != "fp0bbbbbbbbbbbb" {
		t.Fatalf("open 应只剩 b 指纹: %+v", open)
	}
	sup, _ := s.ListPrintsByRepo(ctx, "gitea", "acme", "app", PrintSuppressed, 100)
	if len(sup) != 1 || sup[0].Print != "fp0aaaaaaaaaaaa" {
		t.Fatalf("suppressed 应只剩 a 指纹: %+v", sup)
	}
	// limit
	limited, _ := s.ListPrintsByRepo(ctx, "gitea", "acme", "app", "", 1)
	if len(limited) != 1 {
		t.Fatalf("limit=1 应 1 条, got %d", len(limited))
	}
	// 三计数
	sum, err := s.PrintRepoSummary(ctx, "gitea", "acme", "app")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Total != 2 || sum.Open != 1 || sum.Suppressed != 1 {
		t.Fatalf("汇总应为 total=2 open=1 suppressed=1: %+v", sum)
	}
	// 别仓为空
	other, _ := s.ListPrintsByRepo(ctx, "gitea", "acme", "other", "", 100)
	if len(other) != 0 {
		t.Fatalf("别仓应为空, got %d", len(other))
	}
	// 空仓汇总为零
	zero, _ := s.PrintRepoSummary(ctx, "gitea", "acme", "other")
	if zero.Total != 0 || zero.Open != 0 || zero.Suppressed != 0 {
		t.Fatalf("空仓汇总应为零: %+v", zero)
	}
}
