// budget_reserve.go —— 预算预留账本（#32）：执行前预留 + 调用后核销。
// 一任务一预留行（task_id 唯一，重试原地刷新），status 生命周期：
// reserved（活跃预留，计入已承诺额度）→ settled（任务终态核销）/
// released（崩溃/心跳死亡/过期释放）。expires_at 为租约到期时间——活跃性由
// 心跳续期（TouchHeartbeatsFor），到期未续 = 持有进程已死，由清扫与查询侧
// 双重兜底释放（#32 验收④）。
//
// 额度判定（#32 验收③）：check-then-insert 的跨副本原子性由 leader_leases
// 短租约串行（与迁移租约同机制，key 按 仓×月 切分）——多副本同时启动任务时
// 「都看到 used=0 都放行」的超支窗口在预留临界区内闭合。判定口径：
//
//	配额账已承诺 = done 任务行聚合 + 他任务活跃预留
//	实销已承诺   = usage_event 实销（排除带活跃预留的任务，其潜在消耗已由
//	               预留额度代表，避免同笔双计）+ 他任务活跃预留
//
// 任一账本剩余额度 ≤0 → 拒绝（不落预留行）；通过 → 预留额度 =
// min(任务预算, 各自剩余额度)，对两本账各记一笔承诺。

package storage

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"
)

// reservationTTL 预留租约时长：心跳（缺省 15s 周期）每跳续期一次，5min =
// 20× 心跳周期余量；进程死亡后心跳停 → 预留最迟 5min 过期（正常路径由
// ResetRunningToPending 心跳复位就地释放，不等租约到期）。
const reservationTTL = 5 * time.Minute

// reservationLeaseTTL 预留临界区租约时长：判定+落行毫秒级，10s 覆盖崩溃
// 残留（到期可被他副本接管）。
const reservationLeaseTTL = 10 * time.Second

// reservationLeaseWait 预留租约等待上界：临界区毫秒级，2s 足够排队；
// 超时上抛由调用方 fail-open 放行（沿用旧预算闸「查询失败放行」语义）。
const reservationLeaseWait = 2 * time.Second

// BudgetReservation budget_reservations 表：任务对本仓月度预算的执行前预留。
type BudgetReservation struct {
	ID uint `gorm:"column:id;primaryKey"`

	// TaskID 一任务一行（uniqueIndex）：重试（attempt 递增）原地刷新而非追加，
	// 防同一任务的多次尝试叠加多份预留。
	TaskID    string `gorm:"column:task_id;size:40;not null;uniqueIndex:idx_bres_task"`
	AttemptID string `gorm:"column:attempt_id;size:32"`

	// platform/owner/repo 冗余自 tasks：预算判定按仓聚合，不 JOIN（同 usage_event）。
	Platform string `gorm:"column:platform;size:16;not null;index:idx_bres_repo,priority:1"`
	Owner    string `gorm:"column:owner;size:128;not null;index:idx_bres_repo,priority:2"`
	Repo     string `gorm:"column:repo;size:128;not null;index:idx_bres_repo,priority:3"`

	// Tokens 预留额度（= min(任务预算, 判定时剩余额度)）：配额账与真实消费账
	// 共用同一条预留（对两本账各记一笔承诺额度）。
	Tokens int `gorm:"column:tokens"`

	// Status 预留状态（词表见 Reservation* 常量）。
	Status string `gorm:"column:status;size:16;not null;default:reserved;index"`
	// ExpiresAt 租约到期：活跃预留由心跳续期；到期未续由清扫置 released，
	// 查询侧亦按 expires_at > now 过滤（崩溃恢复双兜底）。
	ExpiresAt time.Time `gorm:"column:expires_at;index"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (BudgetReservation) TableName() string { return "budget_reservations" }

// 预留状态词表。
const (
	// ReservationReserved 活跃预留（计入已承诺额度）。
	ReservationReserved = "reserved"
	// ReservationSettled 任务终态核销（预留生命周期正常结束——任务已收口，
	// 实销由 usage_event 逐调用在账，预留不再代表未来消耗）。
	ReservationSettled = "settled"
	// ReservationReleased 预留释放（心跳复位/租约过期，#32 验收④崩溃恢复）。
	ReservationReleased = "released"
)

// ReserveVerdict 执行前预留判定结果。
type ReserveVerdict int

const (
	// ReserveOK 额度内，预留已落（或无需预留：双账本全关/任务预算非正）。
	ReserveOK ReserveVerdict = iota
	// ReserveQuotaExceeded 配额账（done 口径 + 预留）剩余额度不足。
	ReserveQuotaExceeded
	// ReserveRealExceeded 真实消费账（usage_event 实销 + 预留）剩余额度不足。
	ReserveRealExceeded
)

// ReserveInput 执行前预留入参。
type ReserveInput struct {
	TaskID      string
	AttemptID   string
	Platform    string
	Owner       string
	Repo        string
	TaskBudget  int           // 本任务预算（预留额度上界；≤0 = 无界任务，不占预留）
	QuotaBudget int           // 配额账月度上限（0 = 关）
	RealBudget  int           // 真实消费账月度上限（0 = 关）
	Since       time.Time     // 月度窗口起点（配额/实销聚合口径）
	TTL         time.Duration // 预留租约时长（0 = reservationTTL）
}

// ReserveResult 预留判定输出：Used 字段为拒绝时的已承诺额度（失败播报用）。
type ReserveResult struct {
	Verdict   ReserveVerdict
	QuotaUsed int64 // 配额账已承诺（done 聚合 + 活跃预留）
	RealUsed  int64 // 真实账已承诺（实销 + 活跃预留）
	// GrantedTokens 授予额度（Phase1①，spec §2.1）：截断后的 hold 原样回传——
	// 调用方（runner 预算闸）据此创建任务级 LLM Budget，闭合「账面承诺 5,000
	// 实跑 20,000」的脱节。无需预留路径（双账本全关/任务预算非正）= in.TaskBudget
	// 原值（≤0 表示无界）；拒绝路径 = 0（调用方已拦截，不消费本值）。
	GrantedTokens int64
}

// ReserveRepoBudget 执行前预留（#32 验收③）：双账本额度判定 + 落预留行在同一
// 临界区（leader_leases 短租约串行，跨副本原子）。拒绝 = 不落预留行、由调用方
// 走失败终态播报；双账本全关或任务预算非正时零开销直通（不落行）。
// 存储/租约故障上抛——调用方沿用旧闸「查失败放行」的 fail-open 语义。
func (s *Store) ReserveRepoBudget(ctx context.Context, in ReserveInput) (ReserveResult, error) {
	return reserveRepoBudget(s, ctx, in)
}

// activeHeldTokens 本仓他任务活跃预留合计（status=reserved 且租约未过期）。
func (s *Store) activeHeldTokens(ctx context.Context, platform, owner, repo string, now time.Time, excludeTaskID string) (int64, error) {
	return activeHeldTokens(s, ctx, platform, owner, repo, now, excludeTaskID)
}

// settledNonHeldTokens 实销账已承诺的实销部分：usage_event 实销中「无活跃
// 预留」的任务之和——带活跃预留的任务其潜在消耗由预留额度代表，避免同笔双计。
func (s *Store) settledNonHeldTokens(ctx context.Context, platform, owner, repo string,
	since, now time.Time, excludeTaskID string) (int64, error) {
	return settledNonHeldTokens(s, ctx, platform, owner, repo, since, now, excludeTaskID)
}

// upsertReservation 落/刷新预留行（一任务一行）：先条件更新，无行则插入、
// 撞唯一键（同任务他路径抢先）回退再更新收敛——与 leader_leases 的
// TryAcquire 同形态（MySQL UPDATE 可能回报 0 变更行，幂等重放走此回退）。
func (s *Store) upsertReservation(ctx context.Context, in ReserveInput, tokens int, expires, now time.Time) error {
	return upsertReservation(s, ctx, in, tokens, expires, now)
}

// acquireReservationLease 预留短租约：拿不到 = 他副本正在判定（临界区毫秒级），
// 20ms 轮询至 2s 上界；超时 fail-closed 上抛（调用方 fail-open 放行）。
func (s *Store) acquireReservationLease(ctx context.Context, key, holder string) (func(), error) {
	return acquireReservationLease(s, ctx, key, holder)
}

// reservationLeaseKey 预留租约 key：仓×月切分，fnv64a 哈希压到 128 字符内
// （owner/repo 各可达 128，裸拼会超 leader_leases.lease_key 上限）。哈希碰撞
// 只会让两个无关仓的预留判定串行（额外排队，不影响正确性）。
func reservationLeaseKey(platform, owner, repo string, since time.Time) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s/%s/%s:%s", platform, owner, repo, since.Format("2006-01"))
	return fmt.Sprintf("budget_reserve:%016x", h.Sum64())
}

// RepoMonthlyReserved 本仓当前活跃预留合计（/usage/monthly 预留展示、预算闸
// 与测试断言共用口径）：status=reserved 且租约未过期。
func (s *Store) RepoMonthlyReserved(ctx context.Context, platform, owner, repo string) (int64, error) {
	return repoMonthlyReserved(s, ctx, platform, owner, repo)
}

// MonthlyReservedByRepo 全部仓库活跃预留聚合（/usage/monthly 预留 vs 实销）。
func (s *Store) MonthlyReservedByRepo(ctx context.Context) ([]RepoTokenUsage, error) {
	return monthlyReservedByRepo(s, ctx)
}

// ReleaseExpiredReservations 过期预留清扫（#32 验收④租约/超时路径）：持有
// 进程死亡 → 心跳停 → expires_at 过期 → 置 released 归还额度。查询侧本就按
// expires_at > now 过期（自愈），本清扫只做状态收敛与观测；重复执行幂等。
func (s *Store) ReleaseExpiredReservations(ctx context.Context, now time.Time) (int64, error) {
	return releaseExpiredReservations(s, ctx, now)
}

// releaseTaskReservations 任务维度的预留收口（供 FinishTaskFenced / 复位路径
// 在既有写门内调用——单条 UPDATE，不嵌套 withWrite）。to = settled（终态核销）
// 或 released（复位/崩溃释放）。失败只由调用方告警：预留过期兜底仍在
// （expires_at + 清扫），不阻断任务终态写入。
func (s *Store) releaseTaskReservations(ctx context.Context, taskIDs []string, to string, now time.Time) error {
	return releaseTaskReservations(s, ctx, taskIDs, to, now)
}

// renewTaskReservations 预留租约随心跳续期（供 TouchHeartbeatsFor 等在既有
// 写门内调用）：expires_at 与任务活性绑定——进程活着预留不掉，进程死心跳停
// 预留到期释放。
func (s *Store) renewTaskReservations(ctx context.Context, taskIDs []string, now time.Time) error {
	return renewTaskReservations(s, ctx, taskIDs, now)
}
