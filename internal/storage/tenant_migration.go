// tenant_migration.go —— tenant_scope_v1 一次性迁移（#87）：tenant_id 列回填 +
// 唯一键/主键升维 + 防线索引重建引导。
//
// 执行序（Open 内）：AutoMigrate 已先给自动迁移模型加 tenant_id 列（NOT NULL
// DEFAULT 'default'，存量行自动归默认租户），本迁移在其后跑数据面收口：
//  1. 全表 tenant_id 回填（NULL/'' → 'default'，三方言 ADD COLUMN 语义兜底）；
//  2. 唯一索引/主键升维（tenant 首列）——AutoMigrate 只增不改（同名索引
//     HasIndex 命中即跳过），旧全局唯一键必须显式 drop+create，否则同坐标
//     跨租户互撞（#87 核心缺口）；
//  3. 在飞/同 SHA 防线索引 drop（守卫代码次启动按新定义重建含 tenant 形态）；
//     MySQL 生成列 in_flight_key 一并 DROP（表达式含 tenant，改表达式=删列重建）。
//
// 幂等纪律（迁移可失败重跑）：drop 先按方言探测存在性（PG/SQLite 用
// IF EXISTS，MySQL 无该语法走 information_schema 探测）；create 前先 drop
// 同名。语句全为内联字面量，无运行期插值——迁移 DDL 不含任何用户输入。

package storage

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// tenantDefault 回填值：与 tenant.Default 同字面量（本文件刻意不 import
// tenant 包——迁移属存储层自洽 DDL 面，"default" 字面量由 tenant_normalize
// 测试钉一致）。
const tenantDefault = "default"

// tenantBackfillTables 需回填 tenant_id 的全部业务表（#87 归属面）。
var tenantBackfillTables = []string{
	"tasks", "finding_prints", "pattern_signals", "review_targets",
	"print_aliases", "repo_settings", "qa_sessions", "usage_events",
	"budget_reservations", "event_outbox", "comment_deliveries",
	"fp_case_buckets", "side_effect_release_audits",
	"side_effects", "side_effect_fences",
}

// migrateTenantScope tenant_scope_v1 迁移主函数（登记见 oneTimeMigrations）。
func (s *Store) migrateTenantScope(ctx context.Context) (int, error) {
	return migrateTenantScope(s, ctx)
}

// migrateTenantScope 的包级实现（转发壳见原文件）。
func migrateTenantScope(s *Store, ctx context.Context) (int, error) {
	migrated := 0
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1) 回填（裸 SQL 不经过租户回调，无条件竞争）。
		for _, t := range tenantBackfillTables {
			res := tx.Exec("UPDATE "+t+" SET tenant_id = ? WHERE tenant_id IS NULL OR tenant_id = ''", tenantDefault)
			if res.Error != nil {
				return fmt.Errorf("storage: %s tenant_id 回填失败: %w", t, res.Error)
			}
			migrated += int(res.RowsAffected)
		}
		// 2) 唯一索引升维（drop+create，幂等可重跑）。
		for _, idx := range tenantUniqueIndexes {
			if err := tenantRecreateIndex(tx, s, idx); err != nil {
				return err
			}
		}
		// 3) 主键升维（tenant 并入 PK；SQLite 走 _new 表重建）。
		return tenantWidenPKs(tx, s)
	}); err != nil {
		return 0, err
	}
	// 4) 在飞/同 SHA 防线索引重建（事务外，复用 ensure* 幂等函数——DROP 旧形态
	// 后立即按 tenant 新定义建回：迁移完成即防线齐备，verify 模式复核不等
	// 下一次默认启动；ensure* 函数每启动也会跑，双保险防漂移）。
	if err := tenantRecreateGuardIndexes(s); err != nil {
		return 0, err
	}
	return migrated, nil
}

// tenantUniqueIndex 一张待升维的唯一/复合索引声明。
type tenantUniqueIndex struct {
	table   string
	name    string
	columns string // 逗号分隔列清单（tenant 首列已含）
	unique  bool
}

// tenantUniqueIndexes 全部升维索引（名称与模型 tag 同源——GORM 按 tag 名建
// 索引，本清单重建后 AutoMigrate HasIndex 命中即跳过，防漂移）。
var tenantUniqueIndexes = []tenantUniqueIndex{
	{"tasks", "idx_tasks_event_id", "tenant_id, event_id", true},
	{"tasks", "idx_pr", "tenant_id, platform, owner, repo, number", false},
	{"tasks", "idx_budget", "tenant_id, platform, owner, repo, status, created_at", false},
	{"finding_prints", "idx_print", "tenant_id, platform, owner, repo, number, print", true},
	{"pattern_signals", "idx_pattern", "tenant_id, platform, owner, repo, plugin, pattern_key", true},
	{"review_targets", "idx_target", "tenant_id, platform, owner, repo, number", true},
	{"usage_events", "idx_ue_inv", "tenant_id, invocation_id", true},
	{"usage_events", "idx_ue_budget", "tenant_id, platform, owner, repo, status, occurred_at", false},
	{"usage_events", "idx_ue_task", "tenant_id, task_id", false},
	{"budget_reservations", "idx_bres_task", "tenant_id, task_id", true},
	{"budget_reservations", "idx_bres_repo", "tenant_id, platform, owner, repo", false},
	{"fp_case_buckets", "idx_fp_case_buckets_pattern", "tenant_id, pattern", true},
}

// tenantRecreateIndex 方言化 drop+create 单张索引（MySQL 索引归属表形态
// DROP INDEX <name> ON <table> 且无 IF EXISTS——先探测存在性）。
func tenantRecreateIndex(tx *gorm.DB, s *Store, idx tenantUniqueIndex) error {
	kind := "INDEX"
	if idx.unique {
		kind = "UNIQUE INDEX"
	}
	if s.isMySQL() {
		exists, err := tenantMySQLIndexExists(tx, idx.table, idx.name)
		if err != nil {
			return err
		}
		if exists {
			if err := tx.Exec("DROP INDEX " + idx.name + " ON " + idx.table).Error; err != nil {
				return fmt.Errorf("storage: 降维 drop %s.%s 失败: %w", idx.table, idx.name, err)
			}
		}
		if err := tx.Exec("CREATE " + kind + " " + idx.name + " ON " + idx.table + " (" + idx.columns + ")").Error; err != nil {
			return fmt.Errorf("storage: %s.%s 升维重建失败: %w", idx.table, idx.name, err)
		}
		return nil
	}
	// PG/SQLite：索引全局命名空间，IF EXISTS 幂等。
	if err := tx.Exec("DROP INDEX IF EXISTS " + idx.name).Error; err != nil {
		return fmt.Errorf("storage: 降维 drop %s 失败: %w", idx.name, err)
	}
	if err := tx.Exec("CREATE " + kind + " IF NOT EXISTS " + idx.name + " ON " + idx.table + " (" + idx.columns + ")").Error; err != nil {
		return fmt.Errorf("storage: %s.%s 升维重建失败: %w", idx.table, idx.name, err)
	}
	return nil
}

// tenantMySQLIndexExists information_schema.STATISTICS 探测（MySQL 无
// DROP INDEX IF EXISTS 语法的幂等替代）。
func tenantMySQLIndexExists(tx *gorm.DB, table, name string) (bool, error) {
	var n int64
	if err := tx.Raw("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND INDEX_NAME = ?",
		table, name).Scan(&n).Error; err != nil {
		return false, fmt.Errorf("storage: 探测 %s.%s 索引失败: %w", table, name, err)
	}
	return n > 0, nil
}

// tenantPKWiden 一张主键升维声明（tenant 并入 PK 首列）。
type tenantPKWiden struct {
	table   string
	columns string // 新 PK 列清单（tenant 首列已含）
}

// tenantPKWidens 主键升维清单（qa_sessions/print_aliases/repo_settings/
// comment_deliveries——同坐标跨租户共享主键的四个泄露面）。
var tenantPKWidens = []tenantPKWiden{
	{"qa_sessions", "tenant_id, session_key"},
	{"print_aliases", "tenant_id, platform, owner, repo, number, alias_fp"},
	{"repo_settings", "tenant_id, platform, owner, repo"},
	{"comment_deliveries", "tenant_id, delivery_id"},
}

// tenantWidenPKs 主键升维（方言分流：SQLite 表重建，MySQL/PG 约束替换）。
// 失败由事务回滚兜底——不留「老表已改名新表未建」的半态。
func tenantWidenPKs(tx *gorm.DB, s *Store) error {
	for _, pk := range tenantPKWidens {
		var err error
		switch {
		case s.isSQLite():
			err = tenantRebuildSQLitePK(tx, pk)
		case s.isMySQL():
			err = tx.Exec("ALTER TABLE " + pk.table + " DROP PRIMARY KEY").Error
			if err == nil {
				err = tx.Exec("ALTER TABLE " + pk.table + " ADD PRIMARY KEY (" + pk.columns + ")").Error
			}
		default: // Postgres：pkey 约束名遵循 <table>_pkey 约定。
			err = tx.Exec("ALTER TABLE " + pk.table + " DROP CONSTRAINT IF EXISTS " + pk.table + "_pkey").Error
			if err == nil {
				err = tx.Exec("ALTER TABLE " + pk.table + " ADD PRIMARY KEY (" + pk.columns + ")").Error
			}
		}
		if err != nil {
			return fmt.Errorf("storage: %s 主键升维失败: %w", pk.table, err)
		}
	}
	return nil
}

// tenantRebuildSQLitePK SQLite 主键升维 = _new 表重建（ALTER 不可改 PK 的
// 唯一正解）。列集 = 老列集（tenant_id 剔除防重复）+ 新定义 tenant_id 列；
// INSERT SELECT 全程显式列名不依赖列序；summary 等漂移列以老表实际列集为准。
func tenantRebuildSQLitePK(tx *gorm.DB, pk tenantPKWiden) error {
	tmp := pk.table + "_new"
	oldCols := tenantSQLiteColumns(tx, pk.table)
	if err := tx.Exec("ALTER TABLE " + pk.table + " RENAME TO " + tmp).Error; err != nil {
		return err
	}
	if err := tx.Exec(sqliteCreateStmt(pk.table, pk.columns, oldCols)).Error; err != nil {
		return err
	}
	if err := tx.Exec(sqliteCopyStmt(pk.table, tmp, oldCols)).Error; err != nil {
		return err
	}
	return tx.Exec("DROP TABLE " + tmp).Error
}

// tenantSQLiteColumns 读 SQLite 表现有列名（PRAGMA table_info）。
func tenantSQLiteColumns(tx *gorm.DB, table string) []string {
	rows, err := tx.Raw("PRAGMA table_info(" + table + ")").Rows()
	if err != nil {
		return nil
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil {
			cols = append(cols, name)
		}
	}
	return cols
}

// sqliteCreateStmt 按新主键生成 SQLite CREATE TABLE（tenant_id 列在首；
// 时间列 datetime 亲和，其余 text——与 GORM 建表形态一致）。
func sqliteCreateStmt(table, pkColumns string, oldCols []string) string {
	stmt := `CREATE TABLE "` + table + `" ("tenant_id" text NOT NULL DEFAULT 'default'`
	for _, c := range oldCols {
		if c == "tenant_id" {
			continue
		}
		typ := "text"
		if c == "updated_at" || c == "seen_at" || c == "created_at" {
			typ = "datetime"
		}
		stmt += `, "` + c + `" ` + typ
	}
	stmt += ", PRIMARY KEY (" + pkColumns + "))"
	return stmt
}

// sqliteCopyStmt 老表 → 新表明示列拷贝（tenant_id 回填 'default'）。
func sqliteCopyStmt(table, tmp string, oldCols []string) string {
	var cols, sels []string
	cols = append(cols, `"tenant_id"`)
	sels = append(sels, `'`+tenantDefault+`'`)
	for _, c := range oldCols {
		if c == "tenant_id" {
			continue
		}
		cols = append(cols, `"`+c+`"`)
		sels = append(sels, `"`+c+`"`)
	}
	return `INSERT INTO "` + table + `" (` + joinComma(cols) + ") SELECT " + joinComma(sels) + " FROM " + tmp
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// tenantRecreateGuardIndexes 在飞/同 SHA 防线索引按 tenant 新定义重建：
// 先降维 drop（MySQL 生成列 in_flight_key 表达式需含 tenant——生成列改表达式
// = 删列重建，索引随列自动消亡），再调 ensure* 幂等函数建回。守卫函数每启动
// 亦执行（IF NOT EXISTS 双保险），DDL 定义与守卫代码同源（samesha_guard.go）。
func tenantRecreateGuardIndexes(s *Store) error {
	switch {
	case s.isSQLite():
		for _, name := range []string{sameSHAIndex, sameSHAIndexV2} {
			if err := s.db.Exec("DROP INDEX IF EXISTS " + name).Error; err != nil {
				return fmt.Errorf("storage: 降维 drop %s 失败: %w", name, err)
			}
		}
	case s.isMySQL():
		var colCnt int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = ?",
			inFlightColumnMySQL).Scan(&colCnt).Error; err != nil {
			return fmt.Errorf("storage: 探测 tasks.%s 列失败: %w", inFlightColumnMySQL, err)
		}
		if colCnt > 0 {
			if err := s.db.Exec("ALTER TABLE tasks DROP COLUMN " + inFlightColumnMySQL).Error; err != nil {
				return fmt.Errorf("storage: 降维 drop 生成列 %s 失败: %w", inFlightColumnMySQL, err)
			}
		}
	default: // Postgres
		if err := s.db.Exec("DROP INDEX IF EXISTS " + inFlightIndexPG).Error; err != nil {
			return fmt.Errorf("storage: 降维 drop %s 失败: %w", inFlightIndexPG, err)
		}
	}
	_, err := s.EnsureInFlightGuard()
	return err
}
