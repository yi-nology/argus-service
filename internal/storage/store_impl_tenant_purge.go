// store_impl_tenant_purge.go —— PurgeTenant 的包级实现（#109a 租户级级联清理）。
//
// 退订/法务留存的兑现面：给定租户 ID，把其在**全部租户业务表**中的行物理删除
//（软删 grace 不适用——退订 = 立即清除，没有恢复窗口）。零外键的全库形态下
// 级联只能逐表清（审计票 #109 的现状列），本工具把「逐表手写」收敛为单一入口
// + 表清单与 tenantBackfillTables 同源（加列即自动纳入清理面）。
//
// 谓词口径：Table 裸表形态——租户/软删回调只认 Model schema，裸 Table 天然
// 绕过（Where 只认本函数的 tenant_id 条件，软删行一并物理清除，正是退约语义）。
//
// 事务形态：逐表单 DELETE（不跨表事务）——退约清理是低频运维操作，单表语句
// 失败留已清表计数供人工续跑；跨表大事务反而长锁写通道（SQLite 队头阻塞）。

package storage

import (
	"context"
	"fmt"
)

// purgeTargets 租户业务表清单（裸表名，逐条单 DELETE）。
// 与 tenantBackfillTables 同源——两清单一致性由
// TestPurgeTargetsCoverBackfillTables 钉住（防加表漏清）。
var purgeTargets = []string{
	"tasks", "finding_prints", "pattern_signals", "review_targets",
	"print_aliases", "repo_settings", "qa_sessions", "usage_events",
	"budget_reservations", "event_outbox", "comment_deliveries",
	"fp_case_buckets", "side_effect_release_audits",
	"side_effects", "side_effect_fences",
}

// purgeTenant 的包级实现（转发壳见原文件）。
func purgeTenant(s *Store, ctx context.Context, tenantID string, dryRun bool) (map[string]int64, error) {
	out := map[string]int64{}
	err := s.withWrite(func() error {
		for _, table := range purgeTargets {
			if dryRun {
				var n int64
				if err := s.db.WithContext(ctx).Table(table).
					Where("tenant_id = ?", tenantID).Count(&n).Error; err != nil {
					return fmt.Errorf("purge dry-run %s: %w", table, err)
				}
				if n > 0 {
					out[table] = n
				}
				continue
			}
			res := s.db.WithContext(ctx).Table(table).
				Where("tenant_id = ?", tenantID).Delete(nil)
			if res.Error != nil {
				return fmt.Errorf("purge %s: %w", table, res.Error)
			}
			if res.RowsAffected > 0 {
				out[table] = res.RowsAffected
			}
		}
		return nil
	})
	return out, err
}
