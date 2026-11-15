// store_impl_core.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// tryAcquire 的包级实现（转发壳见原文件）。
func tryAcquire(s *Store, ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	now := time.Now().UTC()
	exp := now.Add(ttl)
	res := s.db.WithContext(ctx).Exec("UPDATE leader_leases SET holder = ?, expires_at = ?, updated_at = ? WHERE lease_key = ? AND (holder = ? OR expires_at < ?)", holder, exp, now, key, holder, now)
	if res.Error != nil {
		return false, res.Error
	}
	if res.RowsAffected > 0 {
		return true, nil
	}
	// INSERT 走 quietDB：行被他实例持有时 INSERT 恒撞唯一键——gorm 默认 logger
	// 会把这条预期内的冲突打红 ERROR（多副本下每次租约操作刷一条，噪声淹没真
	// 故障）。冲突语义由下方回退条件 UPDATE 收敛，v5.4.9 实弹降噪。
	err := s.quietDB().WithContext(ctx).Exec("INSERT INTO leader_leases (lease_key, holder, expires_at, updated_at) VALUES (?, ?, ?, ?)", key, holder, exp, now).Error
	if err == nil {
		return true, nil
	}
	res = s.db.WithContext(ctx).Exec("UPDATE leader_leases SET holder = ?, expires_at = ?, updated_at = ? WHERE lease_key = ? AND (holder = ? OR expires_at < ?)", holder, exp, now, key, holder, now)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// release 的包级实现（转发壳见原文件）。
func release(s *Store, ctx context.Context, key, holder string) error {
	return s.db.WithContext(ctx).Exec("DELETE FROM leader_leases WHERE lease_key = ? AND holder = ?", key, holder).Error
}

// instanceID 的包级实现（转发壳见原文件）。
func instanceID(s *Store) string {
	if s.RunnerID != "" {
		return s.RunnerID
	}
	s.fallbackOnce.Do(func() { s.fallbackID = "fallback-" + uuid.NewString()[:16] })
	return s.fallbackID
}

// isSQLite 的包级实现（转发壳见原文件）。
func isSQLite(s *Store) bool { return s.dialect == DialectSQLite }

// isMySQL 的包级实现（转发壳见原文件）。
func isMySQL(s *Store) bool { return s.dialect == DialectMySQL }

// writeLock 的包级实现（转发壳见原文件）。
func writeLock(s *Store) {
	if s.isSQLite() {
		s.writeMu.Lock()
	}
}

// writeUnlock 的包级实现（转发壳见原文件）。
func writeUnlock(s *Store) {
	if s.isSQLite() {
		s.writeMu.Unlock()
	}
}

// withWrite 的包级实现（转发壳见原文件）。
func withWrite(s *Store, fn func() error) error {
	s.writeLock()
	defer s.writeUnlock()
	return fn()
}

// storeClose 的包级实现（转发壳见原文件）。
func storeClose(s *Store) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// ping 的包级实现（转发壳见原文件）。
func ping(s *Store, ctx context.Context) error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return sqlDB.PingContext(cctx)
}
