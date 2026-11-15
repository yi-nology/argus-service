// store_impl_open.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"fmt"
	"os"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// quietDB 的包级实现（转发壳见原文件）。
func quietDB(s *Store) *gorm.DB {
	return s.db.Session(&gorm.Session{Logger: logger.Discard})
}

// migrateSchema 的包级实现（转发壳见原文件）。
func migrateSchema(s *Store, ctx context.Context) error {
	if s.isSQLite() {
		if err := s.autoMigrateAll(); err != nil {
			return err
		}
		return s.warnInFlightGuard()
	}
	if err := s.ensureLeaseTable(); err != nil {
		return err
	}
	release, err := s.acquireMigrationLease(ctx, automigrateLeaseKey)
	if err != nil {
		return err
	}
	defer release()
	if err := s.autoMigrateAll(); err != nil {
		return err
	}
	return s.warnInFlightGuard()
}

// warnInFlightGuard 的包级实现（转发壳见原文件）。
func warnInFlightGuard(s *Store) error {
	active, err := s.EnsureInFlightGuard()
	s.inFlightGuardActive = active
	switch {
	case err != nil:
		s.db.Logger.Warn(context.Background(), "argus.boot 在飞唯一防线建立失败（双跑防线退化为事务内检查）: %v", err)
		if os.Getenv("ARGUS_STRICT_GUARDS") == "1" {
			return fmt.Errorf("storage: 在飞唯一防线建立失败（ARGUS_STRICT_GUARDS=1 严格模式拒绝启动）: %w", err)
		}
	case !active:
		s.db.Logger.Warn(context.Background(), "argus.boot tasks 表存在历史在飞重复，唯一索引未建立；清理重复任务后重启自愈")
		if os.Getenv("ARGUS_STRICT_GUARDS") == "1" {
			return fmt.Errorf("storage: 在飞唯一防线未建立（历史在飞重复；ARGUS_STRICT_GUARDS=1 严格模式拒绝启动，清理重复任务后重启）")
		}
	}
	return nil
}

// inFlightGuardActive 的包级实现（转发壳见原文件）。
func inFlightGuardActive(s *Store) bool { return s.inFlightGuardActive }

// ensureLeaseTable 的包级实现（转发壳见原文件）。
func ensureLeaseTable(s *Store) error {
	if s.db.Migrator().HasTable(&LeaderLease{}) {
		return nil
	}
	ddl := "CREATE TABLE IF NOT EXISTS leader_leases (" +
		"lease_key varchar(128) PRIMARY KEY, holder varchar(128), " +
		"expires_at "
	if s.isMySQL() {
		ddl += "datetime(3), updated_at datetime(3)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4"
	} else {
		ddl += "timestamptz, updated_at timestamptz)"
	}
	if err := s.db.Exec(ddl).Error; err != nil {
		if s.db.Migrator().HasTable(&LeaderLease{}) {
			return nil // 并发竞态：他副本已建（PG pg_type 撞型形态），表在即放行
		}
		return fmt.Errorf("storage: leader_leases 引导失败: %w", err)
	}
	return nil
}

// autoMigrateAll 的包级实现（转发壳见原文件）。
func autoMigrateAll(s *Store) error {
	if err := s.db.AutoMigrate(autoMigrateModels()...); err != nil {
		return fmt.Errorf("storage: 自动迁移失败: %w", err)
	}
	if s.isMySQL() {
		if err := s.upgradeMySQLLongText(); err != nil {
			return err
		}
	}
	return nil
}

// oneTimeMigrations 的包级实现（转发壳见原文件）。
func oneTimeMigrations(s *Store) []oneTimeMigration {
	return []oneTimeMigration{
		{version: "fingerprint_v2", desc: "指纹 v2", fn: s.migratePrintFingerprintsV2},
		{version: "print_alias_pr_scope", desc: "print_aliases PR 域", fn: s.migratePrintAliasPRScope},
	}
}

// migrateData 的包级实现（转发壳见原文件）。
func migrateData(s *Store, ctx context.Context) error {
	for _, m := range s.oneTimeMigrations() {
		if err := s.runOneTimeMigration(ctx, m.version, m.desc, m.fn); err != nil {
			return err
		}
	}
	// #32 历史实销回填（启动期对账，刻意无哨兵 marker）：滚动发布期间旧二进制
	// 终态的任务、以及终态差额兜底漏网的任务，下次启动自愈；NOT EXISTS 让已有
	// 事件的任务零成本跳过，invocation_id 唯一键兜底幂等。失败语义沿 P0-01
	// fail-closed（ARGUS_MIGRATION_FAIL_OPEN=1 显式降级）——实销断档会让硬预算
	// 低估历史消费。
	n, err := s.ReconcileUsageEvents(ctx)
	if err != nil {
		if migrationFailOpen() {
			s.db.Logger.Warn(ctx, "历史实销回填失败（ARGUS_MIGRATION_FAIL_OPEN=1 fail-open，留待下次启动重试）: %v", err)
			return nil
		}
		return fmt.Errorf("storage: 历史实销回填失败（fail-closed；ARGUS_MIGRATION_FAIL_OPEN=1 可显式降级）: %w", err)
	}
	if n > 0 {
		s.db.Logger.Info(ctx, "历史实销回填完成: %d 行", n)
	}
	return nil
}

// runOneTimeMigration 的包级实现（转发壳见原文件）。
func runOneTimeMigration(s *Store, ctx context.Context, version, desc string, fn func(ctx context.Context) (int, error)) error {
	n, err := s.ensureMigrationApplied(ctx, version, fn)
	if err == nil {
		if n > 0 {
			s.db.Logger.Info(ctx, "%s迁移完成: %d 行", desc, n)
		}
		return nil
	}
	if migrationFailOpen() {
		s.db.Logger.Warn(ctx, "%s迁移失败（ARGUS_MIGRATION_FAIL_OPEN=1 fail-open，留待下次启动重试）: %v", desc, err)
		return nil
	}
	return fmt.Errorf("storage: %s迁移失败（fail-closed；确认可带旧形态数据服务时以 ARGUS_MIGRATION_FAIL_OPEN=1 显式降级）: %w", desc, err)
}

// upgradeMySQLLongText 的包级实现（转发壳见原文件）。
func upgradeMySQLLongText(s *Store) error {
	for table, cols := range longtextColumns {
		for _, col := range cols {
			var cnt int64
			// 探测失败必须暴露：静默跳过 ALTER 会让 64KB 死循环修复无声失效
			if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
				"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ? AND DATA_TYPE = 'text'",
				table, col).Scan(&cnt).Error; err != nil {
				return fmt.Errorf("storage: 探测 %s.%s 列类型失败: %w", table, col, err)
			}
			if cnt == 0 {
				continue
			}
			if err := s.db.Exec(fmt.Sprintf("ALTER TABLE `%s` MODIFY `%s` LONGTEXT", table, col)).Error; err != nil {
				return fmt.Errorf("storage: %s.%s 升级 LONGTEXT 失败: %w", table, col, err)
			}
		}
	}
	return nil
}
