// samesha_guard.go —— 多副本"同 PR 同 SHA 双跑"防线（深挖 P0；P1-03 #8 扩展为
// 三方言在飞唯一约束）：事务内 in-flight Count 检查是单实例纪律（SQLite 单写
// 串行 / writeMu），两个副本的租约过期窗口内可同时读到 0 → 双任务双烧 token。
// 最终防线必须落在数据库约束上。
//
// 设计约束：TaskAPI 手工对同 SHA 的 done 任务重审是合法用途（CreateTaskDirectIfIdle
// "done 任务不拦"），因此不能对全量行建唯一索引——只禁止两份"在飞"任务并存：
//   - SQLite：partial unique index（status IN (pending,running)）；
//   - PostgreSQL：partial unique index（同形态，v6.8.0 P1-03 补齐——生产方言
//     此前零约束，首审并发双建任务双烧 LLM）；
//   - MySQL：无 partial index → in_flight_key 生成列（在飞行拼接身份键、终态行
//     NULL）+ 唯一索引（MySQL 8 生成列；唯一索引对 NULL 天然放行）。
//
// 覆盖组合：webhook 在飞任务（head_sha 非空）按 (platform,owner,repo,number,kind,
// head_sha) 唯一；TaskAPI 手工任务（head_sha 空串）按空 SHA 参与唯一——同 PR 同
// kind 的两份手工在飞任务同样拦截（与 CreateTaskDirectIfIdle 的 in-flight 去重
// 语义对齐）。撞约束后由 CreateTaskForWebhook / CreateTaskDirectIfIdle 的
// isDuplicateKeyErr 回查收敛为幂等语义（不 500）。

package storage

// sameSHAIndex 旧版部分唯一索引名（v3.74.2 前不含 kind）——升级时删除。
const sameSHAIndex = "idx_tasks_same_sha_live"

// sameSHAIndexV2 现行 SQLite 部分唯一索引：含 kind 列——同 PR 不同 kind（审查/
// 写作/CI 归因）的任务允许并存，"在飞唯一"只约束同 kind 同 SHA。
const sameSHAIndexV2 = "idx_tasks_same_sha_live_v2"

// inFlightIndexPG PostgreSQL 部分唯一索引（P1-03 #8）。
const inFlightIndexPG = "idx_tasks_in_flight"

// inFlightColumnMySQL / inFlightIndexMySQL MySQL 生成列与唯一索引名。
const (
	inFlightColumnMySQL = "in_flight_key"
	inFlightIndexMySQL  = "idx_tasks_in_flight"
)

// EnsureInFlightGuard 建立在飞任务唯一防线（三方言）。active=false + err=nil
// 表示因历史在飞重复未能建立——双跑防线退化为事务内检查，调用方应告警；在飞
// 任务收敛后重启自愈。active=false + err≠nil 为真实执行错误（权限/磁盘等），
// 调用方须带原始错误告警（v3.9.1：此前整体吞掉，运维会按"历史在飞重复"的
// hint 清数据、永远无效）。
// 调用时点：storage.Open 的迁移租约内（AutoMigrate 之后）——建索引/加列是 DDL，
// 多副本并发执行必须串行（与 P0-04 automigrate 租约同窗）。
func (s *Store) EnsureInFlightGuard() (active bool, err error) { return ensureInFlightGuard(s) }

// EnsureSameSHAGuard 兼容别名（v6.8.0 更名 EnsureInFlightGuard：防线从 SQLite
// 专属扩为三方言在飞唯一约束）。
func (s *Store) EnsureSameSHAGuard() (bool, error) { return ensureSameSHAGuard(s) }

// checkInFlightGuard 在飞防线只读复核（#29 verify 模式专用）：查索引/生成列
// 是否已建立，不执行任何 DDL——verify 的「只校验不迁移」承诺由本函数承担。
// 返回 (active, err)：active=false + err=nil = 防线不存在（历史重复未建或
// Job 未跑），语义与 EnsureInFlightGuard 的未建立态对齐；err≠nil = 探测本身失败。
func (s *Store) checkInFlightGuard() (bool, error) { return checkInFlightGuard(s) }

// ensureSQLiteSameSHAGuard 既有 SQLite 部分唯一索引（v3.74.2 形态不变）。
func (s *Store) ensureSQLiteSameSHAGuard() (bool, error) { return ensureSQLiteSameSHAGuard(s) }

// ensurePGInFlightGuard PostgreSQL 部分唯一索引（P1-03 #8）：与 SQLite 同形态。
func (s *Store) ensurePGInFlightGuard() (bool, error) { return ensurePGInFlightGuard(s) }

// ensureMySQLInFlightGuard MySQL 生成列 + 唯一索引（P1-03 #8）：in_flight_key =
// 在飞行按 CONCAT_WS 拼接身份键（STORED 生成列），终态行恒 NULL（唯一索引对
// NULL 放行 = done/failed/stale 不受限）。幂等：information_schema 探测列与
// 索引存在性，缺哪步补哪步（AutoMigrate 不感知该列，滚动升级不回删）。
func (s *Store) ensureMySQLInFlightGuard() (bool, error) { return ensureMySQLInFlightGuard(s) }
