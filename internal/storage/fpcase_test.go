package storage

import (
	"context"
	"testing"

	"github.com/yi-nology/argus/internal/domain"
)

// TestFPCaseBucketLifecycle 桶生命周期（批 E）：upsert 聚合 HitCount++ →
// candidate 待审 → confirm → confirmed 命中 / rejected 不命中。
func TestFPCaseBucketLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pat := domain.FPCasePattern("gitea", "acme", "a.go", "口令硬编码")

	// 首次落 candidate + 来源锚
	if err := s.UpsertFPCase(ctx, FPCaseBucket{
		Pattern: pat, Kind: "a.go", Org: "acme",
		SampleRepo: "acme/repo1", SamplePrint: "aaaa000000000001",
		Comment: "口令硬编码", HitCount: 1,
	}); err != nil {
		t.Fatalf("首次 upsert: %v", err)
	}
	// 再抑制（另一仓同模式）→ HitCount++
	if err := s.UpsertFPCase(ctx, FPCaseBucket{
		Pattern: pat, Kind: "a.go", Org: "acme",
		SampleRepo: "acme/repo2", SamplePrint: "aaaa000000000002",
		Comment: "口令硬编码", HitCount: 1,
	}); err != nil {
		t.Fatalf("二次 upsert: %v", err)
	}
	cands, err := s.ListFPCaseCandidates(ctx, 0)
	if err != nil || len(cands) != 1 {
		t.Fatalf("candidate = %d err=%v, want 1", len(cands), err)
	}
	if cands[0].HitCount != 2 {
		t.Fatalf("HitCount = %d, want 2（跨仓聚合）", cands[0].HitCount)
	}

	// candidate 不参与命中（红线：只提示 confirmed）
	if hit, err := s.MatchFPCase(ctx, pat); err != nil || hit != nil {
		t.Fatalf("candidate 不应命中: hit=%v err=%v", hit, err)
	}

	// 人工确认 → confirmed 命中
	if err := s.ConfirmFPCase(ctx, pat, domain.FPCaseConfirmed); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	hit, err := s.MatchFPCase(ctx, pat)
	if err != nil || hit == nil || hit.Org != "acme" {
		t.Fatalf("confirmed 应命中: hit=%+v err=%v", hit, err)
	}

	// 非法状态拒
	if err := s.ConfirmFPCase(ctx, pat, "confirmed"); err == nil {
		t.Fatal("非法状态应拒（须 candidate 态进入）")
	}
}
