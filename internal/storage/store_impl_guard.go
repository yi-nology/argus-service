// store_impl_guard.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"fmt"
)

// ensureInFlightGuard 的包级实现（转发壳见原文件）。
func ensureInFlightGuard(s *Store) (active bool, err error) {
	switch {
	case s.isSQLite():
		return s.ensureSQLiteSameSHAGuard()
	case s.dialect == DialectPostgres:
		return s.ensurePGInFlightGuard()
	case s.dialect == DialectMySQL:
		return s.ensureMySQLInFlightGuard()
	}
	return false, fmt.Errorf("storage: 未知方言 %v，在飞唯一防线未建立", s.dialect)
}

// ensureSameSHAGuard 的包级实现（转发壳见原文件）。
func ensureSameSHAGuard(s *Store) (bool, error) { return s.EnsureInFlightGuard() }

// checkInFlightGuard 的包级实现（转发壳见原文件）。
func checkInFlightGuard(s *Store) (bool, error) {
	switch {
	case s.isSQLite():
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?",
			sameSHAIndexV2).Scan(&n).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 SQLite 防线索引失败: %w", err)
		}
		return n > 0, nil
	case s.dialect == DialectPostgres:
		var n int64
		if err := s.db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE tablename='tasks' AND indexname=?",
			inFlightIndexPG).Scan(&n).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 PG 防线索引失败: %w", err)
		}
		return n > 0, nil
	case s.dialect == DialectMySQL:
		var colCnt int64
		if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = ?",
			inFlightColumnMySQL).Scan(&colCnt).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 MySQL 防线列失败: %w", err)
		}
		if colCnt == 0 {
			return false, nil
		}
		var idxCnt int64
		if err := s.db.Raw("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS "+
			"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = ?",
			inFlightIndexMySQL).Scan(&idxCnt).Error; err != nil {
			return false, fmt.Errorf("storage: 探测 MySQL 防线索引失败: %w", err)
		}
		return idxCnt > 0, nil
	}
	return false, fmt.Errorf("storage: 未知方言 %v，无法复核在飞防线", s.dialect)
}

// ensureSQLiteSameSHAGuard 的包级实现（转发壳见原文件）。
func ensureSQLiteSameSHAGuard(s *Store) (bool, error) {
	// v3.74.2 索引升级：v1（不含 kind）删除，重建 v2（含 kind）。DROP 幂等，
	// 每次启动执行一次，在飞行数少无锁窗风险。
	_ = s.db.Exec("DROP INDEX IF EXISTS " + sameSHAIndex).Error
	// 幂等：IF NOT EXISTS 对已存在的同名索引（含非唯一形态）不重建
	cerr := s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + sameSHAIndexV2 +
		" ON tasks(platform, owner, repo, number, kind, head_sha) " +
		"WHERE status IN ('pending', 'running')").Error
	if cerr == nil {
		// v2 建成后清一次 v1 残留：滚动升级窗口里旧副本可能按旧镜像重启把 v1
		// 复活（CREATE IF NOT EXISTS），v1 存在会误杀跨 kind 同 SHA 并发（v2 明确
		// 放行的组合）。DROP 幂等零成本，每次启动顺带执行。
		_ = s.db.Exec("DROP INDEX IF EXISTS " + sameSHAIndex).Error
		return true, nil
	}
	if isDuplicateKeyErr(cerr) {
		// 历史在飞重复会让唯一索引创建失败：不阻断启动（数据不满足约束，
		// 非执行错误——调用方按数据冲突告警后照常服务）
		return false, nil
	}
	return false, cerr
}

// ensurePGInFlightGuard 的包级实现（转发壳见原文件）。
func ensurePGInFlightGuard(s *Store) (bool, error) {
	cerr := s.db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS " + inFlightIndexPG +
		" ON tasks(platform, owner, repo, number, kind, head_sha) " +
		"WHERE status IN ('pending', 'running')").Error
	if cerr == nil {
		return true, nil
	}
	if isDuplicateKeyErr(cerr) {
		// 历史在飞重复（pre-v6.8.0 双建产物）阻塞建索引：不阻断启动，告警自愈路径
		return false, nil
	}
	return false, cerr
}

// ensureMySQLInFlightGuard 的包级实现（转发壳见原文件）。
func ensureMySQLInFlightGuard(s *Store) (bool, error) {
	var colCnt int64
	if err := s.db.Raw("SELECT COUNT(*) FROM information_schema.COLUMNS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND COLUMN_NAME = ?",
		inFlightColumnMySQL).Scan(&colCnt).Error; err != nil {
		return false, fmt.Errorf("storage: 探测 tasks.%s 列失败: %w", inFlightColumnMySQL, err)
	}
	if colCnt == 0 {
		add := "ALTER TABLE tasks ADD COLUMN " + inFlightColumnMySQL +
			" VARCHAR(512) GENERATED ALWAYS AS (CASE WHEN status IN ('pending','running') " +
			"THEN CONCAT_WS('|', platform, owner, repo, number, kind, head_sha) ELSE NULL END) STORED"
		if err := s.db.Exec(add).Error; err != nil {
			return false, fmt.Errorf("storage: tasks.%s 生成列创建失败: %w", inFlightColumnMySQL, err)
		}
	}
	var idxCnt int64
	if err := s.db.Raw("SELECT COUNT(DISTINCT INDEX_NAME) FROM information_schema.STATISTICS "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'tasks' AND INDEX_NAME = ? AND NON_UNIQUE = 0",
		inFlightIndexMySQL).Scan(&idxCnt).Error; err != nil {
		return false, fmt.Errorf("storage: 探测 %s 索引失败: %w", inFlightIndexMySQL, err)
	}
	if idxCnt == 0 {
		if err := s.db.Exec("ALTER TABLE tasks ADD UNIQUE INDEX " + inFlightIndexMySQL +
			" (" + inFlightColumnMySQL + ")").Error; err != nil {
			if isDuplicateKeyErr(err) {
				// 历史在飞重复（pre-v6.8.0 双建产物）阻塞建唯一索引：不阻断启动
				return false, nil
			}
			return false, fmt.Errorf("storage: tasks.%s 唯一索引创建失败: %w", inFlightIndexMySQL, err)
		}
	}
	return true, nil
}
