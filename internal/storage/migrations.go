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
