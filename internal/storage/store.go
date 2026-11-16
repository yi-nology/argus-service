// store.go —— Store 生命周期（打开/关闭/Ping）、身份与方言判定、进程内写门。

package storage

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
)

// Store 存储门面。SQLite 下用连接数=1 + 写锁实现"查询与落库同一事务"的串行语义
// （design §5.2：SQLite 单写串行下单事务即闭环）；MySQL/pg 下并发正确性由行锁/
// SKIP LOCKED/唯一索引承担，进程级互斥只制造吞吐天花板，写锁仅 SQLite 生效
// （v3.53.0 writeLock/writeUnlock）。
type Store struct {
	db      *gorm.DB
	writeMu sync.Mutex
	dialect Dialect // 方言单源（v6.2.0）：写门/行锁/迁移 DDL 分流全看它
	// RunnerID 本实例标识（Open 后由 main 赋值；claim/心跳用它归属 running 任务）
	RunnerID string
	// fallbackID 迁移锁 holder 的兜底身份（评估 P0 修复）：RunnerID 未装配
	//（测试/工具直连）时进程内惰性生成唯一 ID。holder 相同会让 TryAcquire 的
	// `holder = ? OR expires_at < ?` 条件互相"续约"抢锁（双实例并发首启同跑迁移）。
	fallbackOnce sync.Once
	fallbackID   string
	// inFlightGuardActive 在飞唯一防线建立结果（v6.12 P1 readyz 探针用；
	// warnInFlightGuard 在 Open 时写入，正常启动后即为真实值）。
	inFlightGuardActive bool
}

// instanceID 本实例唯一身份：RunnerID 优先，未装配时惰性生成进程内唯一 ID
// （同进程多 Store 实例也能区分 holder）。
func (s *Store) instanceID() string { return instanceID(s) }

// isSQLite/isMySQL 方言判定（全包唯一出处——此前 Store.sqlite/Store.mysql 双
// bool 与 isSQLite(tx) 三源并存，见 dialect.go 头注释）。
func (s *Store) isSQLite() bool { return isSQLite(s) }
func (s *Store) isMySQL() bool  { return isMySQL(s) }

// writeLock/writeUnlock 进程内写串行门（v3.53.0 SQLite 感知）：writeMu 为 SQLite
// 单写者语义而生。MySQL 路径各写点的并发正确性均有 DB 侧机制承担——claim 走
// SKIP LOCKED 事务、webhook 建任务对 target 行 FOR UPDATE、幂等靠唯一索引；
// 单任务执行体唯一，attempt 读改写与 checkpoint 合并无同任务并发。多副本
// MySQL 横向扩容前的必要解锁。
func (s *Store) writeLock() { writeLock(s) }

func (s *Store) writeUnlock() { writeUnlock(s) }

// withWrite 写门内执行 fn（v6.2.0 结构化封装）：取锁与 defer 解锁闭合在本函数，
// 「忘 defer 解锁 → SQLite 写锁永久持有」从纪律问题变成语法上不可能。此前
// writeLock/defer writeUnlock 两行配对散落 30 处，全靠人肉对齐。多返回值站点
// 以具名结果在闭包内赋值；嵌套调用带写门的方法会死锁（sync.Mutex 不可重入），
// 调用方须自查（现状 30 处无嵌套，收编不改变锁覆盖范围）。
func (s *Store) withWrite(fn func() error) error { return withWrite(s, fn) }

// ErrNotFound 查询无结果的边界哨兵：上层判 404/无此对象以此为准，不再感知
// gorm.ErrRecordNotFound——ORM 错误语义不出 storage 包。
var ErrNotFound = errors.New("storage: record not found")

// gormPlugin 可选 GORM 插件（v3.163.0 oekit/tracing 接入缝）：组合根在 Open 前
// 经 SetGormPlugin 注入（gormotel），Open 后 db.Use 挂载——DB 查询成 OTel span。
// nil = 不挂（缺省，零开销）。
var gormPlugin gorm.Plugin

// SetGormPlugin 注入 GORM 插件（须在 Open 前调用；后调用者生效）。
func SetGormPlugin(p gorm.Plugin) { gormPlugin = p }

// Close 关闭底层连接。
func (s *Store) Close() error { return storeClose(s) }

// Ping 健康检查（readyz）。
func (s *Store) Ping(ctx context.Context) error { return ping(s, ctx) }
