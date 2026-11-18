// migrations.go —— 一次性迁移的哨兵机制（v3.8.0）。
//
// 背景：本项目 schema 演进靠 GORM AutoMigrate（幂等增量），但"数据回写型"迁移
// （如指纹 v2 全表重算）不能每次启动都跑——大表全扫 + 半迁移态带病服务。
// SchemaMigration 表记录已应用的一次性迁移版本，fn 成功后才落 marker。
// 注意：marker 语义是"该库已迁移到该版本"；旧二进制接同库属于版本倒挂
// （<v3.7.0 读不懂 v2 指纹，历史抑制会全部失配复活），属 runbook 明令禁止的
// 回滚红线，代码侧无法两全。

package storage

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// SchemaMigration 一次性数据迁移的应用记录。
type SchemaMigration struct {
	Version   string    `gorm:"column:version;primaryKey;size:64"`
	AppliedAt time.Time `gorm:"column:applied_at"`
}

func (SchemaMigration) TableName() string { return "schema_migrations" }

// migrationLockTTL 迁移锁租期：覆盖重建型迁移执行上界（printalias 秒级~十秒级，
// 5min 余量充足）；续约（每 TTL/10）兜住长迁移，崩溃时锁按 TTL 自然过期。
const migrationLockTTL = 5 * time.Minute

// automigrateLeaseKey AutoMigrate/LONGTEXT 升级的迁移租约 key（P0-04，#5）：
// schema DDL 与数据回写迁移共用 leader_leases 机制，key 域区分。
const automigrateLeaseKey = "schema_migration:automigrate"

// migrationRenewalInterval 迁移租约续约间隔（缺省 TTL/10）。atomic：fencing 注入
// 测试缩短到毫秒级——续约环 goroutine 的读与测试 cleanup 的恢复写之间无 happens-before
// 边（-race 实锤），普通包级 var 是内存模型违规。
var migrationRenewalInterval atomic.Int64

func init() { migrationRenewalInterval.Store(int64(migrationLockTTL / 10)) }

// HookMigrationLeaseLost 迁移租约失租观测钩（P2-05，#16）：fencing 触发时调用
// （续约失败/被夺/终检失败）。nil 安全；obs.Init 注入 prometheus 计数——storage
// 不 import obs（arch 白名单纪律，与 plugin.Hook* 同模式）。
var HookMigrationLeaseLost func()

// acquireMigrationLease 抢迁移租约，阻塞至持有或超时（P0-04，#5）：拿不到 =
// 他副本正在迁移（多副本并发首启），让位轮询等待——AutoMigrate 无 marker 可等，
// 只能等锁本身；超时 fail-closed 拒绝启动。返回释放函数。
func (s *Store) acquireMigrationLease(ctx context.Context, key string) (func(), error) {
	return acquireMigrationLease(s, ctx, key)
}

// awaitMigrationMarker 让位等待（评估 P0）：抢锁失败 = 他实例正在跑该迁移。
// 轮询 marker 出现（每 2s，上界 = TTL + 2min 余量）：等到 = 视为已应用返回 0；
// 超时 = fail-closed 报错拒绝启动（提示人工核查，不带病服务）；ctx 取消透传。
func (s *Store) awaitMigrationMarker(ctx context.Context, version string) (int, error) {
	return awaitMigrationMarker(s, ctx, version)
}

// ensureMigrationApplied 幂等执行一次性迁移：marker 已存在 → 跳过（返回 0, nil）；
// fn 成功后才写 marker——失败不写，下次启动重试。
func (s *Store) ensureMigrationApplied(ctx context.Context, version string, fn func(ctx context.Context) (int, error)) (applied int, err error) {
	return ensureMigrationApplied(s, ctx, version, fn)
}

// ensureSideEffectTables sideeffect 闸的两张表（C1）。刻意不进 AutoMigrate 模型
// 清单：DDL 需要方言化（MySQL 索引内联 / PG、SQLite 独立 IF NOT EXISTS），且
// 结果列是 LONGBLOB/BYTEA，交给 GORM 推断只会得到方言漂移的 schema。
//
//	side_effect_fences(resource PK, fence, holder, lease_until)   -- 资源闸 + 令牌
//	side_effects(effect_key PK, resource, status, retry_safety, payload_hash,
//	             holder, fence, lease_until, result, reason,
//	             recon_holder, recon_until, created_at, updated_at)
//	INDEX (resource, status, lease_until)                         -- 资源占用判定
//
// 并发首启：CREATE TABLE IF NOT EXISTS 服务端幂等；PG 并发建表仍可能在系统
// catalog 撞唯一键（与 leader_leases 同形态）——失败后复核两表都在即放行。
func (s *Store) ensureSideEffectTables() error {
	tables, rest := sideEffectDDL(s.dialect)
	for _, stmt := range tables {
		if err := s.db.Exec(stmt).Error; err != nil {
			if s.db.Migrator().HasTable("side_effect_fences") && s.db.Migrator().HasTable("side_effects") {
				continue // 并发竞态：他副本已建（表在即放行，列型由本次 DDL 预算兜）
			}
			return fmt.Errorf("storage: side_effect 表引导失败: %w", err)
		}
	}
	// tenant_id 列存量补齐（#87）：表已存在的老库无该列——三方言探测 + ADD。
	for _, t := range []string{"side_effect_fences", "side_effects"} {
		if err := s.ensureTenantColumn(t); err != nil {
			return err
		}
	}
	for _, stmt := range rest {
		if err := s.db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("storage: side_effect 索引引导失败: %w", err)
		}
	}
	return nil
}

// ensureTenantColumn 探测并补齐一张表的 tenant_id 列（存量库升级面；新库
// CREATE TABLE 已含本列，探测命中即零成本跳过）。
func (s *Store) ensureTenantColumn(table string) error {
	exists := false
	switch {
	case s.isSQLite():
		rows, err := s.db.Raw("PRAGMA table_info(" + table + ")").Rows()
		if err != nil {
			return fmt.Errorf("storage: 探测 %s 列失败: %w", table, err)
		}
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err == nil && name == "tenant_id" {
				exists = true
			}
		}
		rows.Close()
	case s.dialect == DialectPostgres:
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = CURRENT_SCHEMA() AND TABLE_NAME = ? AND COLUMN_NAME = 'tenant_id'",
			table).Scan(&n).Error; err != nil {
			return fmt.Errorf("storage: 探测 %s.tenant_id 失败: %w", table, err)
		}
		exists = n > 0
	default: // MySQL：CURRENT_SCHEMA() 是 PG 专属函数，MySQL 用 DATABASE()。
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'tenant_id'",
			table).Scan(&n).Error; err != nil {
			return fmt.Errorf("storage: 探测 %s.tenant_id 失败: %w", table, err)
		}
		exists = n > 0
	}
	if exists {
		return nil
	}
	ddl := "ALTER TABLE " + table + " ADD COLUMN tenant_id "
	if s.isMySQL() {
		ddl += "VARCHAR(64) NOT NULL DEFAULT 'default'"
	} else {
		ddl += "TEXT NOT NULL DEFAULT 'default'"
	}
	if err := s.db.Exec(ddl).Error; err != nil {
		return fmt.Errorf("storage: %s 补 tenant_id 列失败: %w", table, err)
	}
	return nil
}

// sideEffectDDL 三方言建表/建索引语句（tables 先行，indexes 后置）。
func sideEffectDDL(d Dialect) (tables, indexes []string) {
	switch d {
	case DialectMySQL:
		tables = []string{
			`CREATE TABLE IF NOT EXISTS side_effect_fences (
				resource VARCHAR(255) NOT NULL,
				fence BIGINT NOT NULL DEFAULT 0,
				holder VARCHAR(64) NOT NULL DEFAULT '',
				lease_until DATETIME(3) NULL,
				updated_at DATETIME(3) NULL,
				PRIMARY KEY (resource)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
			`CREATE TABLE IF NOT EXISTS side_effects (
				effect_key VARCHAR(255) NOT NULL,
				resource VARCHAR(255) NOT NULL,
				status VARCHAR(16) NOT NULL DEFAULT 'pending',
				retry_safety SMALLINT NOT NULL DEFAULT 0,
				payload_hash VARCHAR(64) NOT NULL DEFAULT '',
				holder VARCHAR(64) NULL,
				fence BIGINT NULL,
				lease_until DATETIME(3) NULL,
				result LONGBLOB NULL,
				reason TEXT NULL,
				recon_holder VARCHAR(64) NULL,
				recon_until DATETIME(3) NULL,
				created_at DATETIME(3) NULL,
				updated_at DATETIME(3) NULL,
				PRIMARY KEY (effect_key),
				KEY idx_side_effects_resource (resource, status, lease_until)
			) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
		}
	case DialectPostgres:
		tables = []string{
			`CREATE TABLE IF NOT EXISTS side_effect_fences (
				resource VARCHAR(255) PRIMARY KEY,
				fence BIGINT NOT NULL DEFAULT 0,
				holder VARCHAR(64) NOT NULL DEFAULT '',
				lease_until TIMESTAMPTZ,
				updated_at TIMESTAMPTZ
			)`,
			`CREATE TABLE IF NOT EXISTS side_effects (
				effect_key VARCHAR(255) PRIMARY KEY,
				resource VARCHAR(255) NOT NULL,
				status VARCHAR(16) NOT NULL DEFAULT 'pending',
				retry_safety SMALLINT NOT NULL DEFAULT 0,
				payload_hash VARCHAR(64) NOT NULL DEFAULT '',
				holder VARCHAR(64),
				fence BIGINT,
				lease_until TIMESTAMPTZ,
				result BYTEA,
				reason TEXT,
				recon_holder VARCHAR(64),
				recon_until TIMESTAMPTZ,
				created_at TIMESTAMPTZ,
				updated_at TIMESTAMPTZ
			)`,
		}
		indexes = []string{
			`CREATE INDEX IF NOT EXISTS idx_side_effects_resource ON side_effects (resource, status, lease_until)`,
		}
	default:
		tables = []string{
			`CREATE TABLE IF NOT EXISTS side_effect_fences (
				resource TEXT PRIMARY KEY,
				fence INTEGER NOT NULL DEFAULT 0,
				holder TEXT NOT NULL DEFAULT '',
				lease_until DATETIME,
				updated_at DATETIME
			)`,
			`CREATE TABLE IF NOT EXISTS side_effects (
				effect_key TEXT PRIMARY KEY,
				resource TEXT NOT NULL,
				status TEXT NOT NULL DEFAULT 'pending',
				retry_safety INTEGER NOT NULL DEFAULT 0,
				payload_hash TEXT NOT NULL DEFAULT '',
				holder TEXT,
				fence INTEGER,
				lease_until DATETIME,
				result BLOB,
				reason TEXT,
				recon_holder TEXT,
				recon_until DATETIME,
				created_at DATETIME,
				updated_at DATETIME
			)`,
		}
		indexes = []string{
			`CREATE INDEX IF NOT EXISTS idx_side_effects_resource ON side_effects (resource, status, lease_until)`,
		}
	}
	return tables, indexes
}
