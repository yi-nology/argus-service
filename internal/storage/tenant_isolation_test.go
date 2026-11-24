// tenant_isolation_test.go —— tenant_scope_v1 机制验证（#87/#94）：
// 同坐标资源跨租户互不撞键、互相不可见；Default 租户行为与旧单租户完全一致。

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/tenant"
)

// openTenantTestStore 内存 SQLite + 全量迁移（同真实 Open 路径）。
func openTenantTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open("file:" + t.TempDir() + "/t.db")
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	s.RunnerID = "test-runner"
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestTenantSameCoordinateIsolation 同坐标（platform/owner/repo/number）跨租户：
// 任务双建不撞在飞防线、事件幂等键不互吞、prints/QA 会话/repo_settings 各归各。
func TestTenantSameCoordinateIsolation(t *testing.T) {
	s := openTenantTestStore(t)
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	ctxB := tenant.NewContext(context.Background(), "tenant-b")

	// 任务：同坐标同 SHA 双租户并建——tenant 首列的在飞唯一防线放行（#87 验收）。
	taskA := &Task{ID: "t-aaa", Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web",
		Number: "7", HeadSHA: "sha1", Status: StatusPending}
	taskB := &Task{ID: "t-bbb", Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web",
		Number: "7", HeadSHA: "sha1", Status: StatusPending}
	if err := s.db.WithContext(ctxA).Create(taskA).Error; err != nil {
		t.Fatalf("租户 A 建任务: %v", err)
	}
	if err := s.db.WithContext(ctxB).Create(taskB).Error; err != nil {
		t.Fatalf("同坐标租户 B 建任务应成功（tenant 首列防线）: %v", err)
	}

	// 事件幂等键：同 delivery ID 两租户互不吞（per-tenant 唯一）。
	ev := "delivery-1"
	ta := &Task{ID: "t-a2", Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web",
		Number: "8", EventID: &ev, Status: StatusDone}
	tb := &Task{ID: "t-b2", Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web",
		Number: "8", EventID: &ev, Status: StatusDone}
	if err := s.db.WithContext(ctxA).Create(ta).Error; err != nil {
		t.Fatalf("租户 A 建事件任务: %v", err)
	}
	if err := s.db.WithContext(ctxB).Create(tb).Error; err != nil {
		t.Fatalf("同 delivery ID 租户 B 应不撞 idx_tasks_event_id: %v", err)
	}

	// 可见性：A 读不到 B 的任务（谓词注入机制面）。
	var count int64
	if err := s.db.WithContext(ctxA).Model(&Task{}).Where("id = ?", "t-bbb").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("租户 A 不应看见租户 B 的任务，got %d", count)
	}

	// QA 会话：同 session_key 两租户各存各（复合主键）。
	qaA := &QASession{SessionKey: "gitea:acme/web#7", HistoryJSON: `[{"q":"a的提问"}]`}
	qaB := &QASession{SessionKey: "gitea:acme/web#7", HistoryJSON: `[{"q":"b的提问"}]`}
	if err := s.db.WithContext(ctxA).Create(qaA).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctxB).Create(qaB).Error; nil != err {
		t.Fatalf("同 session_key 租户 B 存会话应不撞主键: %v", err)
	}
	got, err := s.GetQASession(ctxA, "gitea:acme/web#7", time.Hour)
	if err != nil || got == nil {
		t.Fatalf("租户 A 应读回本租户会话: %v", err)
	}
	if got.HistoryJSON != qaA.HistoryJSON {
		t.Fatalf("会话串租户: got %s", got.HistoryJSON)
	}

	// repo_settings：同坐标配置互不覆盖。
	rsA := &RepoSetting{Platform: "gitea", Owner: "acme", Repo: "web", SettingsJSON: `{"monthly_token_budget":1000}`}
	rsB := &RepoSetting{Platform: "gitea", Owner: "acme", Repo: "web", SettingsJSON: `{"monthly_token_budget":2000}`}
	if err := s.UpsertRepoSetting(ctxA, rsA); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertRepoSetting(ctxB, rsB); err != nil {
		t.Fatal(err)
	}
	ra, err := s.GetRepoSetting(ctxA, "gitea", "acme", "web")
	if err != nil || ra == nil {
		t.Fatalf("租户 A 读配置: %v", err)
	}
	if ra.SettingsJSON != rsA.SettingsJSON {
		t.Fatalf("配置串租户: got %s", ra.SettingsJSON)
	}

	// 意见治理读面（v8.5.0）：同坐标 prints 跨租户互不可见、计数各归各。
	f := domain.Finding{File: "a.go", Line: 1, Severity: "high", Comment: "x", By: "correctness"}
	if _, err := s.UpsertPrint(ctxA, "gitea", "acme", "web", "7", "fp0aaaaaaaaaaaa", f, "t-aaa", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertPrint(ctxB, "gitea", "acme", "web", "7", "fp0bbbbbbbbbbbb", f, "t-bbb", time.Now()); err != nil {
		t.Fatal(err)
	}
	rowsA, err := s.ListPrintsByRepo(ctxA, "gitea", "acme", "web", "", 100)
	if err != nil || len(rowsA) != 1 || rowsA[0].Print != "fp0aaaaaaaaaaaa" {
		t.Fatalf("租户 A 应只见本租户指纹: rows=%+v err=%v", rowsA, err)
	}
	sumB, err := s.PrintRepoSummary(ctxB, "gitea", "acme", "web")
	if err != nil || sumB.Total != 1 {
		t.Fatalf("租户 B 计数应只算本租户: %+v err=%v", sumB, err)
	}
	rowsDefault, err := s.ListPrintsByRepo(context.Background(), "gitea", "acme", "web", "", 100)
	if err != nil || len(rowsDefault) != 0 {
		t.Fatalf("default 租户不应看见 tenant-a/b 指纹: rows=%+v err=%v", rowsDefault, err)
	}
}

// TestTenantDefaultTenantBackwardCompat 无 ctx 租户声明（旧调用/内部路径）=
// Default 租户，行为与单租户时代一致（回填口径）。
func TestTenantDefaultTenantBackwardCompat(t *testing.T) {
	s := openTenantTestStore(t)
	ctx := context.Background()
	task := &Task{ID: "t-def", Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web",
		Number: "1", Status: StatusPending}
	if err := s.db.WithContext(ctx).Create(task).Error; err != nil {
		t.Fatal(err)
	}
	var got Task
	if err := s.db.WithContext(ctx).Where("id = ?", "t-def").First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.TenantID != tenant.Default {
		t.Fatalf("缺省租户回填应为 %q, got %q", tenant.Default, got.TenantID)
	}
}

// TestTenantWithoutScopeEscape tenant.WithoutScope 逃生门：跨租户对账/计数面。
func TestTenantWithoutScopeEscape(t *testing.T) {
	s := openTenantTestStore(t)
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	ctxB := tenant.NewContext(context.Background(), "tenant-b")
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if err := s.db.WithContext(ctx).Create(&Task{ID: "esc-" + tenant.FromContext(ctx),
			Kind: "cr", Platform: "gitea", Owner: "acme", Repo: "web", Number: "9",
			Status: StatusPending}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var all int64
	if err := s.db.WithContext(tenant.WithoutScope(context.Background())).
		Model(&Task{}).Where("id LIKE 'esc-%'").Count(&all).Error; err != nil {
		t.Fatal(err)
	}
	if all != 2 {
		t.Fatalf("逃生门应跨租户计数 2, got %d", all)
	}
}

// TestTenantSideEffectScoped side_effect 谓词注入面：同资源串下两租户各自的
// 动作行互不读串（资源闸跨租户共享是 #83 应用层键前缀的活——本测试只钉存储
// 半边：tenant_id 列回填 + 锁查询谓词）。
func TestTenantSideEffectScoped(t *testing.T) {
	s := openTenantTestStore(t)
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	ctxB := tenant.NewContext(context.Background(), "tenant-b")
	now := time.Now()
	rowA := &sideEffectRow{EffectKey: "fx-a", Resource: "pr:gitea:acme/web#7",
		Status: sideEffectStatusDone, PayloadHash: "h", CreatedAt: now, UpdatedAt: now}
	rowB := &sideEffectRow{EffectKey: "fx-b", Resource: "pr:gitea:acme/web#7",
		Status: sideEffectStatusDone, PayloadHash: "h", CreatedAt: now, UpdatedAt: now}
	if err := s.db.WithContext(ctxA).Create(rowA).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctxB).Create(rowB).Error; err != nil {
		t.Fatal(err)
	}
	var got sideEffectRow
	if err := s.db.WithContext(ctxA).Where("effect_key = ?", "fx-b").First(&got).Error; err == nil {
		t.Fatalf("租户 A 不应读到租户 B 的动作行")
	}
	var reload sideEffectRow
	if err := s.db.WithContext(ctxB).Where("effect_key = ?", "fx-b").First(&reload).Error; err != nil {
		t.Fatalf("租户 B 应读本租户动作行: %v", err)
	}
	if reload.TenantID != "tenant-b" {
		t.Fatalf("回填租户应为 tenant-b, got %q", reload.TenantID)
	}
}

// TestTenantClaimCrossTenant 调度面跨租户（#88）：非 default 租户 pending 任务
// 必须能被 worker 池认领——谓词注入在 claim 面旁路，runner 归属即隔离边界。
func TestTenantClaimCrossTenant(t *testing.T) {
	s := openTenantTestStore(t)
	ctxB := tenant.NewContext(context.Background(), "tenant-b")
	if err := s.db.WithContext(ctxB).Create(&Task{ID: "t-claim", Kind: "cr",
		Platform: "gitea", Owner: "acme", Repo: "web", Number: "9",
		Status: StatusPending, Priority: 0}).Error; err != nil {
		t.Fatal(err)
	}
	// 认领 ctx 无租户声明（worker 池形态）——调度面必须跨租户。
	task, ok, err := s.ClaimNextPending(context.Background())
	if err != nil || !ok {
		t.Fatalf("跨租户认领失败: ok=%v err=%v", ok, err)
	}
	if task.ID != "t-claim" || task.TenantID != "tenant-b" {
		t.Fatalf("认领错任务: %+v", task)
	}
	// 心跳续期/复位同口径（runner_id 归属边界）。
	if err := s.TouchRunningHeartbeats(context.Background()); err != nil {
		t.Fatalf("跨租户心跳续期: %v", err)
	}
}

// TestShareRevocation 撤销清单（#91）：写入后命中判定，跨租户可见（全局清单面）。
func TestShareRevocation(t *testing.T) {
	s := openTenantTestStore(t)
	ctx := context.Background()
	revoked, err := s.IsShareRevoked(ctx, "task-1")
	if err != nil || revoked {
		t.Fatalf("初始应未撤销: %v %v", revoked, err)
	}
	if _, err := s.RevokeShare(ctx, "task-1", "泄漏轮换", "fp-1"); err != nil {
		t.Fatal(err)
	}
	// 撤销判定跨 ctx 租户可见（拒绝清单是全局面，与 share 面域语义一致）。
	ctxB := tenant.NewContext(context.Background(), "tenant-b")
	if revoked, _ := s.IsShareRevoked(ctxB, "task-1"); !revoked {
		t.Fatal("撤销后应命中（跨租户可见）")
	}
	if revoked, _ := s.IsShareRevoked(ctx, "task-2"); revoked {
		t.Fatal("未撤销 subject 不得命中")
	}
	rows, err := s.ListShareRevocations(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].Subject != "task-1" {
		t.Fatalf("清单面: %+v %v", rows, err)
	}
}
