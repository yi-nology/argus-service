// store_impl_migrations.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/oekit/concurrency/async"
	"gorm.io/gorm"
)

// verifySchemaReady 的包级实现（转发壳见原文件）。
func verifySchemaReady(s *Store, ctx context.Context) error {
	for _, m := range autoMigrateModels() {
		if !s.db.Migrator().HasTable(m) {
			return fmt.Errorf("storage: ARGUS_MIGRATE_MODE=verify 校验失败：表（模型 %T）缺失——先以 ARGUS_MIGRATE_MODE=job 跑 Migration Job", m)
		}
	}
	// C1 副作用闸表（方言化 DDL，不在 AutoMigrate 模型清单）：verify 副本同样
	// fail-closed——旧 Migration Job 建的库没有这两张表，缺表时回帖闸会在运行期
	// 才暴露（fail-soft 退化 = at-most-once 静默失效）。
	for _, table := range []string{"side_effect_fences", "side_effects"} {
		if !s.db.Migrator().HasTable(table) {
			return fmt.Errorf("storage: ARGUS_MIGRATE_MODE=verify 校验失败：表 %s 缺失——先以 ARGUS_MIGRATE_MODE=job 跑 Migration Job", table)
		}
	}
	for _, m := range s.oneTimeMigrations() {
		var n int64
		if err := s.db.WithContext(ctx).Model(&SchemaMigration{}).
			Where("version = ?", m.version).Count(&n).Error; err != nil {
			return fmt.Errorf("storage: verify 复核回写哨兵 %s 失败: %w", m.version, err)
		}
		if n == 0 {
			return fmt.Errorf("storage: ARGUS_MIGRATE_MODE=verify 校验失败：数据迁移 %s(%s) 未执行——先以 ARGUS_MIGRATE_MODE=job 跑 Migration Job",
				m.version, m.desc)
		}
	}
	// 在飞防线只读复核（不建索引）：active 状态供 /readyz 严格模式探针消费，
	// 缺失语义与 warnInFlightGuard 同——严格模式拒绝、非严格告警放行。
	active, err := s.checkInFlightGuard()
	s.inFlightGuardActive = active
	if err != nil {
		return fmt.Errorf("storage: verify 复核在飞防线失败: %w", err)
	}
	if !active {
		if os.Getenv("ARGUS_STRICT_GUARDS") == "1" {
			return fmt.Errorf("storage: ARGUS_MIGRATE_MODE=verify 校验失败：在飞唯一防线未建立（历史在飞重复或 Job 未完成；ARGUS_STRICT_GUARDS=1 拒绝启动）")
		}
		s.db.Logger.Warn(ctx, "argus.boot verify: 在飞唯一防线未建立（历史在飞重复；清理重复任务后重启自愈）")
	}
	return nil
}

// acquireMigrationLease 的包级实现（转发壳见原文件）。
func acquireMigrationLease(s *Store, ctx context.Context, key string) (func(), error) {
	holder := s.instanceID() + ":migration:" + key
	deadline := time.Now().Add(migrationLockTTL + 2*time.Minute)
	for {
		ok, err := s.TryAcquire(ctx, key, holder, migrationLockTTL)
		if err != nil {
			return nil, fmt.Errorf("storage: 迁移租约 %s 获取失败: %w", key, err)
		}
		if ok {
			return func() { _ = s.Release(context.Background(), key, holder) }, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("storage: 等待迁移租约 %s 超时（他实例持有未释放，疑似迁移中崩溃）：请人工核查数据库后重启", key)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// awaitMigrationMarker 的包级实现（转发壳见原文件）。
func awaitMigrationMarker(s *Store, ctx context.Context, version string) (int, error) {
	deadline := time.Now().Add(migrationLockTTL + 2*time.Minute)
	var marker SchemaMigration
	for {
		err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error
		if err == nil {
			return 0, nil
		}
		if err != gorm.ErrRecordNotFound {
			return 0, err
		}
		if !time.Now().Before(deadline) {
			return 0, fmt.Errorf("等待迁移 %s 超时（他实例持有迁移锁但 marker 未落，疑似迁移中崩溃）：请人工核查 schema_migrations 与数据一致性后重启", version)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ensureMigrationApplied 的包级实现（转发壳见原文件）。
func ensureMigrationApplied(s *Store, ctx context.Context, version string, fn func(ctx context.Context) (int, error)) (applied int, err error) {
	var marker SchemaMigration
	if err := s.db.WithContext(ctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	// 单飞锁（v5.4.8 多副本加固；v6.3.0 评估 P0 修复）：leader_leases 租约互斥——
	// 多副本（pg/MySQL 网络库）同时首启时防并发跑重建型迁移（printalias PR 域
	// 迁移是 DROP+CREATE，并发执行会互相踩）。
	//
	// ⚠️ 修复要点（外部评估实锤）：lease key 全实例固定，holder 必须唯一——
	// v6.3.0 前 holder = "schema_migration:"+version 全实例相同，TryAcquire 的
	// `holder = ? OR expires_at < ?` 条件在 holder 相同时让后启动实例直接把锁
	// 从执行方手里"续约"抢走 → 双实例并发首启同跑迁移（DDL 冲突/数据不一致）。
	// ① holder = instanceID()+":migration:"+version（key 与 holder 分离）。
	// ② 拿不到锁 = 他实例正在跑：让位等 marker 出现（等到=已应用；超时
	// fail-closed 拒绝启动——带半迁移态服务比启动失败更危险），不再静默返回 0
	// （v6.3.0 前的静默让位会让调用方误以为已应用，带着半迁移态直接服务）。
	// ③ 执行期续约：长迁移超 TTL 时锁过期，第三实例可闯入双跑；每 TTL/10 续约。
	key := "schema_migration:" + version
	holder := s.instanceID() + ":migration:" + version
	ok, lerr := s.TryAcquire(ctx, key, holder, migrationLockTTL)
	if lerr != nil {
		return 0, lerr
	}
	if !ok {
		return s.awaitMigrationMarker(ctx, version)
	}
	defer func() { _ = s.Release(ctx, key, holder) }()
	// ③ 执行期续约 + fencing（v6.8.0 P1-01，#6）：长迁移超 TTL 时锁过期，第三
	// 实例可闯入双跑（同一迁移两个有效执行者 → DDL/数据回写互相踩踏）。续约环
	// 每 TTL/10 续约；**续约失败（err≠nil，库断连等）或返回 ok=false（租约被夺/
	// 过期被抢）= 失租**——fencing 取消迁移执行（fn ctx 贯穿，cancel 传导），结果
	// 作废不写 marker（成功才写幂等语义），下次启动重试、他实例接手。v6.8.0 前
	// 续约失败被吞（return 出环继续跑）且 ok 返回值不检查，失租后执行方无感知。
	//
	// 收敛纪律（死锁教训）：主路绝不阻塞等续约环退出——环随 fctx 生命周期终止
	//（fcancel defer 于函数返回时触发）；失租态经 atomic.Bool 传递；marker 写入
	// 前的「终检续约」补齐强语义（fn 成功与写 marker 之间若恰失租，终检 ok=false
	// 拦下，marker 不落）。
	fctx, fcancel := context.WithCancel(ctx)
	defer fcancel()
	var leaseLost atomic.Bool
	async.GoSafe(func() {
		t := time.NewTicker(time.Duration(migrationRenewalInterval.Load()))
		defer t.Stop()
		for {
			select {
			case <-fctx.Done():
				return
			case <-t.C:
				ok, err := s.TryAcquire(context.Background(), key, holder, migrationLockTTL)
				if err != nil || !ok {
					leaseLost.Store(true)
					if HookMigrationLeaseLost != nil {
						HookMigrationLeaseLost()
					}
					fcancel() // fencing：失租即取消迁移执行
					return
				}
			}
		}
	})
	// 拿到锁后双检 marker（他实例可能已跑完写 marker）。
	if err := s.db.WithContext(fctx).Where("version = ?", version).First(&marker).Error; err == nil {
		return 0, nil
	} else if err != gorm.ErrRecordNotFound {
		return 0, err
	}
	applied, err = fn(fctx)
	if err != nil {
		if leaseLost.Load() {
			applied = 0
			return 0, fmt.Errorf("迁移 %s 执行期间迁移租约丢失（fencing），结果作废不写 marker，待下次启动重试", version)
		}
		return applied, err
	}
	// 终检续约（fencing 强语义）：fn 成功后、写 marker 前再续一次——仍持有 = 刚
	// 续成功的租约覆盖 marker 写入窗口，接手者的 double-check 会看到 marker；
	// 失租（被夺/断连）= 结果作废不写 marker。终检后到 marker 落库之间的残余
	// 窗口（微秒级）由 TTL（5min）量级兜底：接手者只能在本实例 Release 后进入，
	// 届时 marker 已可被 double-check 命中。
	if ok, lerr := s.TryAcquire(context.Background(), key, holder, migrationLockTTL); lerr != nil || !ok {
		if HookMigrationLeaseLost != nil {
			HookMigrationLeaseLost()
		}
		return 0, fmt.Errorf("迁移 %s marker 落库前终检续约失败（fencing，ok=%v err=%v），结果作废待重试", version, ok, lerr)
	}
	if err := s.db.WithContext(fctx).Create(&SchemaMigration{Version: version, AppliedAt: time.Now()}).Error; err != nil {
		return applied, err
	}
	return applied, nil
}

// migrateSideEffectPayload 一次性迁移：side_effects 补 payload 身份载荷列。
// 对账按完整身份裁决（Runner.Reconcile→ensureIdentity）需要载荷字节重算
// 哈希；历史行 NULL = 载荷从未落库，身份面退化为 Resource+RetrySafety
// （见 ensureIdentity 双 nil 跳过分支——仍强于按 Key 裁决）。幂等：已有列跳过。
func (s *Store) migrateSideEffectPayload(_ context.Context) (int, error) {
	if s.db.Migrator().HasColumn(&sideEffectRow{}, "payload") {
		return 0, nil
	}
	ddl := "ALTER TABLE side_effects ADD COLUMN payload BLOB NULL"
	if s.dialect == DialectPostgres {
		ddl = "ALTER TABLE side_effects ADD COLUMN payload BYTEA NULL"
	}
	if err := s.db.Exec(ddl).Error; err != nil {
		return 0, fmt.Errorf("storage: side_effects 补 payload 列失败: %w", err)
	}
	return 0, nil
}
