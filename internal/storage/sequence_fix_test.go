package storage

// sequence_fix_test.go —— PG 序列对齐回归（生产实锤：dbcopy 显式 ID 灌库后
// 序列停在低位，新插入永久 23505——finding_prints 曾静默两周）。
// 仅在 ARGUS_TEST_PG_DSN 提供时执行（与 pg 方言矩阵同门禁）。

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRepairSequencesFixesExplicitIDCopy PG：显式 ID 灌入（模拟 dbcopy）→
// 序列落后 → 修复后 nextval 越过 max(id)，新插入不再撞键。
func TestRepairSequencesFixesExplicitIDCopy(t *testing.T) {
	if os.Getenv("ARGUS_TEST_PG_DSN") == "" {
		t.Skip("ARGUS_TEST_PG_DSN 未设置（pg 方言矩阵专用）")
	}
	s := openTestStore(t)
	ctx := context.Background()
	if s.dialect != DialectPostgres {
		t.Skip("仅 PG 需要序列修复")
	}
	// 模拟显式 ID 灌库（dbcopy 的 Create 显式 ID 同形态）：直接用原始 SQL 写
	// 显式 id——不经自增序列，序列停在低位（生产事故形态复刻）。
	for i := 1; i <= 5; i++ {
		if err := s.db.WithContext(ctx).Exec(
			`INSERT INTO usage_events (id, task_id, invocation_id, platform, owner, repo, status, occurred_at, created_at, updated_at, tenant_id)
			 VALUES (?, ?, ?, 'gitea', 'o', 'r', 'settled', now(), now(), now(), 'default')`,
			100+i, "seqfix", "inv-seqfix-"+string(rune('a'+i))).Error; err != nil {
			t.Fatalf("显式 ID 灌入: %v", err)
		}
	}
	// 未修复前：序列自 1 起，nextval 落在 1..105 内必撞 → 修复必须把它推到 106。
	repaired, err := s.RepairSequences(ctx)
	if err != nil {
		t.Fatalf("RepairSequences: %v", err)
	}
	found := false
	for _, tb := range repaired {
		if tb == "usage_events" {
			found = true
		}
	}
	if !found {
		t.Fatalf("usage_events 应被修复（显式 ID 灌入后序列落后）: %v", repaired)
	}
	// 修复后普通插入（走自增）必须成功且 id > 105。
	ev := &UsageEvent{TaskID: "seqfix2", InvocationID: "inv-seqfix-new", Platform: "gitea",
		Owner: "o", Repo: "r", Status: UsageSettled, OccurredAt: time.Now()}
	if err := s.db.WithContext(ctx).Create(ev).Error; err != nil {
		t.Fatalf("修复后插入失败（序列仍落后）: %v", err)
	}
	if ev.ID <= 105 {
		t.Fatalf("修复后新 id=%d 应 > 105", ev.ID)
	}
	// 幂等：再跑一次不再报修复。
	again, err := s.RepairSequences(ctx)
	if err != nil {
		t.Fatalf("二次 RepairSequences: %v", err)
	}
	for _, tb := range again {
		if tb == "usage_events" {
			t.Fatalf("二次运行不应再修复 usage_events: %v", again)
		}
	}
}
