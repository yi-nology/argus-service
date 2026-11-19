package storage

// #109a 软删除契约测试：谓词回调自动排除/逃生门可见/两阶段 prune/恢复/
// event_id 释放/级联清理工具。机制改动（callback、prune、purge）任一回归
// 都在这里红。

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/tenant"
)

func softDelTask(id, status string, created time.Time) *Task {
	return &Task{
		ID: id, Kind: "code_review", Platform: "gitea", Owner: "o", Repo: "r",
		Number: "1", Status: status, CreatedAt: created, UpdatedAt: created,
	}
}

func TestPruneTasksSoftMarksAndHides(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	mustCreate(t, s, softDelTask("sd-old-done", StatusDone, old))
	mustCreate(t, s, softDelTask("sd-old-failed", StatusFailed, old))
	mustCreate(t, s, softDelTask("sd-new-pending", StatusPending, time.Now()))

	n, err := s.PruneTasks(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("第一阶软标 n=%d err=%v, want 2", n, err)
	}
	// 常规查询不可见（GetTask 命中谓词回调）。
	if _, err := s.GetTask(ctx, "sd-old-done"); err == nil {
		t.Fatal("软删任务应被常规查询隐藏")
	}
	// 计数面同样排除。
	depths, err := s.TaskCountsByTenant(tenant.WithoutScope(ctx))
	if err != nil || depths["default"][0] != 1 {
		t.Fatalf("队列计数应排除软删行: %v err=%v", depths, err)
	}
	// WithDeleted 逃生门可见。
	seen, err := s.RestoreTask(tenant.WithDeleted(ctx), "sd-old-done")
	if err != nil || !seen {
		t.Fatalf("WithDeleted 下应命中软删行: seen=%v err=%v", seen, err)
	}
	// 恢复后常规查询复活。
	if _, err := s.GetTask(ctx, "sd-old-done"); err != nil {
		t.Fatalf("恢复后任务应可见: %v", err)
	}
}

func TestPruneTasksFreesEventID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	ev := "delivery-reuse-1"
	t1 := softDelTask("sd-ev", StatusDone, old)
	t1.EventID = &ev
	mustCreate(t, s, t1)
	if _, err := s.PruneTasks(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// 软删已清 event_id：同 delivery ID 重建任务不得被判幂等去重（NULL 不占唯一键）。
	t2 := softDelTask("sd-ev-reborn", StatusPending, time.Now())
	t2.EventID = &ev
	dup, _, err := s.CreateTaskForWebhook(ctx, t2)
	if err != nil || dup != "" {
		t.Fatalf("同 event_id 重建应成功: dup=%q err=%v", dup, err)
	}
}

func TestPruneTasksHardPhysicallyRemoves(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	mustCreate(t, s, softDelTask("sd-hard", StatusDone, old))
	if _, err := s.PruneTasks(ctx, time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// grace 未到：硬删不动。
	if n, err := s.PruneTasksHard(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("grace 内硬删应为 0: n=%d err=%v", n, err)
	}
	// grace 已过：物理清除，WithDeleted 也不再可见。graceCutoff 语义 =「软删
	// 时刻早于本值的行清除」——模拟宽限流逝用未来值（等价于现在再退 7 天看）。
	if n, err := s.PruneTasksHard(ctx, time.Now().Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("grace 后硬删 n=%d err=%v, want 1", n, err)
	}
	if _, err := s.GetTask(tenant.WithDeleted(ctx), "sd-hard"); err == nil {
		t.Fatal("硬删后 WithDeleted 也不可见")
	}
}

func TestPurgeTenantCascade(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	// default 租户两任务 + acme 一任务 + acme 会话。
	mustCreate(t, s, softDelTask("pg-def-1", StatusDone, now))
	acmeTask := softDelTask("pg-acme-1", StatusDone, now)
	acmeTask.TenantID = "acme" // Create 回调按 ctx 回填——直接造行须显式带归属
	mustCreate(t, s, acmeTask)
	acmeCtx := tenant.NewContext(ctx, "acme")
	if err := s.SaveQASession(acmeCtx, &QASession{SessionKey: "pg-acme-s", TenantID: "acme"}); err != nil {
		t.Fatal(err)
	}
	// dry-run：只计数。
	counts, err := s.PurgeTenant(ctx, "acme", true)
	if err != nil || counts["tasks"] != 1 || counts["qa_sessions"] != 1 {
		t.Fatalf("dry-run 计数失真: %v err=%v", counts, err)
	}
	if _, err := s.GetTask(acmeCtx, "pg-acme-1"); err != nil {
		t.Fatal("dry-run 不应落刀")
	}
	// 实际清理。
	if _, err := s.PurgeTenant(ctx, "acme", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(acmeCtx, "pg-acme-1"); err == nil {
		t.Fatal("acme 任务应已清除")
	}
	if _, err := s.GetTask(ctx, "pg-def-1"); err != nil {
		t.Fatal("default 租户任务不得误伤")
	}
	var n int64
	if err := s.db.Model(&QASession{}).Where("tenant_id = ?", "acme").Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("acme 会话应已清除: n=%d err=%v", n, err)
	}
}

// TestPurgeTargetsCoverBackfillTables 清单一致性钉约：purgeTargets 必须与
// tenantBackfillTables 同源——加业务表进回填清单却漏清理面 = 退约残留。
func TestPurgeTargetsCoverBackfillTables(t *testing.T) {
	purge := map[string]bool{}
	for _, tb := range purgeTargets {
		purge[tb] = true
	}
	for _, tb := range tenantBackfillTables {
		if !purge[tb] {
			t.Errorf("回填表 %s 未纳入 purgeTargets（退约会残留）", tb)
		}
	}
}

func mustCreate(t *testing.T, s *Store, task *Task) {
	t.Helper()
	if err := s.db.Create(task).Error; err != nil {
		t.Fatalf("造任务 %s: %v", task.ID, err)
	}
}
