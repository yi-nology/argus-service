// prints_migrate_test.go —— 指纹 v2 与 print_aliases PR 域迁移测试。

package storage

import (
	"context"
	"testing"
	"time"

	akseverity "github.com/oagentkit/textx/reportutil"

	"github.com/yi-nology/argus/internal/domain"
)

// 存量迁移：v1 格式行按 file/line/comment 重算为 v2（幂等；第二次运行为零）。
func TestMigratePrintFingerprintsV2_RekeysLegacyRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	v1 := akseverity.Fingerprint("a.go", "存量误报意见") // 旧格式（无行号、无版本域）
	now := time.Now()
	if err := s.db.Create(&FindingPrint{
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7", Print: v1,
		File: "a.go", Line: 42, Severity: "medium", Comment: "存量误报意见",
		Status: PrintSuppressed, FirstTaskID: "old", LastTaskID: "old",
		TimesSeen: 4, RegretCount: 2, FirstSeenAt: now, LastSeenAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	n, err := s.migratePrintFingerprintsV2(ctx)
	if err != nil || n != 1 {
		t.Fatalf("应迁移 1 行，got n=%d err=%v", n, err)
	}
	var row FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "7").
		First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if want := domain.Fingerprint("a.go", "存量误报意见", 42); row.Print != want {
		t.Fatalf("应重算为 v2 指纹: got %s want %s", row.Print, want)
	}
	// 抑制状态与计数原地保留
	if row.Status != PrintSuppressed || row.TimesSeen != 4 || row.RegretCount != 2 {
		t.Fatalf("迁移不得丢状态/计数: %+v", row)
	}
	// 幂等：重跑为零
	if n, err := s.migratePrintFingerprintsV2(ctx); err != nil || n != 0 {
		t.Fatalf("迁移应幂等，got n=%d err=%v", n, err)
	}
}

// v3.8.0 回归：v1 时代 regret 幽灵行（空 file/comment，Print 为各自改写后身份）
// 重算 v2 后同指纹——迁移必须合并而不是撞唯一索引死循环。
func TestMigratePrintFingerprintsV2_CollisionMerges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	// 两条空内容行（模拟 v1 时代 RecordPrintRegret 幽灵行），Print 是任意互异的
	// 旧格式值——重算后都等于 Fingerprint("","",0) 同一常量。
	for _, old := range []string{"aaaa1111aaaa1111aaaa1111aaaa1111", "bbbb2222bbbb2222bbbb2222bbbb2222"} {
		if err := s.db.Create(&FindingPrint{
			Platform: "gitea", Owner: "acme", Repo: "app", Number: "8", Print: old,
			Status: PrintOpen, RegretCount: 1,
			FirstTaskID: "reflect", LastTaskID: "reflect",
			FirstSeenAt: now, LastSeenAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.migratePrintFingerprintsV2(ctx)
	if err != nil {
		t.Fatalf("碰撞组应合并成功而非唯一索引冲突: %v", err)
	}
	if n != 2 { // 计数口径：合并组删除 1 行 + 主行 print 回写 1 次
		t.Fatalf("应合并掉 1 行并回写主行（n=2），got n=%d", n)
	}
	var rows []FindingPrint
	if err := s.db.Where("platform=? AND owner=? AND repo=? AND number=?", "gitea", "acme", "app", "8").
		Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应只剩 1 行，got %d", len(rows))
	}
	if rows[0].Print != domain.Fingerprint("", "", 0) || rows[0].RegretCount != 1 {
		t.Fatalf("合并行指纹/regret 应保留: %+v", rows[0])
	}
}

// v3.9.2 迁移回填：旧形态（alias_fp 单列主键、无 PR 列）→ PR 域复合主键。
// 旧行按 print_fp 反查 PR——多 PR 命中逐 PR 复制，孤儿（规范行已清理）丢弃。
func TestMigratePrintAliasPRScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Open() 已按新形态建表并跑过迁移（空表、marker 已写）；重建为旧形态造存量
	if err := s.db.Exec(`DROP TABLE print_aliases`).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Exec(`CREATE TABLE print_aliases (alias_fp varchar(64) PRIMARY KEY, print_fp varchar(64), updated_at timestamp)`).Error; err != nil {
		t.Fatal(err)
	}
	// 规范行：同指纹 Fx 在 PR 7 与 PR 8 各一条；孤儿别名指向的 Gx 不存在
	fx, gx, a1, a2 := "fx0000000001", "gx0000000001", "a10000000001", "a20000000002"
	for _, number := range []string{"7", "8"} {
		// 时间字段显式赋值（MySQL 严格模式拒 '0000-00-00' 零值 datetime——CI 矩阵实锤）。
		if err := s.db.Create(&FindingPrint{Platform: "gitea", Owner: "acme", Repo: "app",
			Number: number, Print: fx, File: "a.go", Line: 1, Severity: "high",
			Comment: "注入风险", Status: PrintOpen,
			FirstSeenAt: time.Now(), LastSeenAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := s.db.Exec(`INSERT INTO print_aliases (alias_fp, print_fp, updated_at) VALUES ('` + a1 + `', '` + fx + `', CURRENT_TIMESTAMP), ('` + a2 + `', '` + gx + `', CURRENT_TIMESTAMP)`).Error; err != nil {
		t.Fatal(err)
	}

	n, err := s.migratePrintAliasPRScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("迁移后应剩 2 行（A1 复制到两 PR、孤儿 A2 丢弃），got %d", n)
	}
	// 各 PR 的 A1 都解析到本 PR 规范行（PrintState 不含指纹字段，以行内容佐证）
	for _, number := range []string{"7", "8"} {
		if st, err := s.GetPrintState(ctx, "gitea", "acme", "app", number, a1); err != nil || st == nil || st.Comment != "注入风险" {
			t.Fatalf("PR %s 应经别名解析到 Fx 行，got %+v err=%v", number, st, err)
		}
	}
}
