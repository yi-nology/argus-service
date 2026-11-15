// side_effects.go —— sideeffect.Store + OwnedResolveStore 的 GORM 实现（C1）：
// 评论/PR/分支推送等创建型副作用的跨进程「至多一次」闸。
//
// 两份状态、三张事实（与 kernel sideeffect.Store 契约逐条对应）：
//   - Key 级（side_effects 一行）：动作身份（resource/retry_safety/payload_hash）、
//     执行意图（holder/fence/lease_until）、终态（status: done/uncertain/pending）、
//     结果与对账占用（recon_holder/recon_until）。
//   - 资源级（side_effect_fences 一行）：当前持有者 holder + 租约到期 lease_until +
//     已发放的最大栅栏令牌 fence。令牌**只在这里**单调递增。
//
// 原子性：每个方法 = 一个事务 = 先锁资源行、再锁动作行（锁序恒定，MySQL/PG 用
// SELECT … FOR UPDATE；SQLite 由 Store.writeMu + 单写者语义串行）。判定与写入
// 同事务 ⇒ 校验失败即整体回滚，不会留下「意图撤了但未知没标上」这类半截状态。
package storage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// side_effects.status 词表：只有 done/uncertain 是终态，其余一律视为「无结论」。
const (
	sideEffectStatusPending   = "pending"
	sideEffectStatusDone      = "done"
	sideEffectStatusUncertain = "uncertain"
)

// sideEffectRow Key 级状态的一行（列语义见 migrations.go 的 DDL 注释）。
//
// holder/fence/lease_until 是**执行意图**（Key 级），不是资源闸：Release 只清
// 资源闸、不动这里；Abandon/Complete/MarkUncertain 才裁决意图。
type sideEffectRow struct {
	EffectKey   string     `gorm:"column:effect_key;primaryKey;size:255"`
	Resource    string     `gorm:"column:resource;size:255;not null"`
	Status      string     `gorm:"column:status;size:16;not null"`
	RetrySafety int16      `gorm:"column:retry_safety;not null"`
	PayloadHash string     `gorm:"column:payload_hash;size:64;not null"`
	Holder      *string    `gorm:"column:holder;size:64"`
	Fence       *int64     `gorm:"column:fence"`
	LeaseUntil  *time.Time `gorm:"column:lease_until"`
	Result      []byte     `gorm:"column:result"`
	Reason      string     `gorm:"column:reason"`
	ReconHolder *string    `gorm:"column:recon_holder;size:64"`
	ReconUntil  *time.Time `gorm:"column:recon_until"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
}

func (sideEffectRow) TableName() string { return "side_effects" }

// hasIntent 该 Key 上是否挂着一次未裁决的尝试。
func (r *sideEffectRow) hasIntent() bool { return r.Fence != nil }

// sideEffectFenceRow 资源级状态的一行：当前持有者 + 租约 + 已发放的最大令牌。
type sideEffectFenceRow struct {
	Resource   string     `gorm:"column:resource;primaryKey;size:255"`
	Fence      int64      `gorm:"column:fence;not null"`
	Holder     string     `gorm:"column:holder;size:64;not null"`
	LeaseUntil *time.Time `gorm:"column:lease_until"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

func (sideEffectFenceRow) TableName() string { return "side_effect_fences" }

// held 该资源当前是否被占着且租约未过期（零值 holder = 空闲）。
func (r *sideEffectFenceRow) held(now time.Time) bool {
	return r.Holder != "" && r.LeaseUntil != nil && r.LeaseUntil.After(now)
}

// SideEffectStore sideeffect.Store 的宿主实现。嵌入 *Store 复用连接/方言/写门；
// 独立类型是因为 Store 自己已有一个 leader_leases 的 Release 方法。
type SideEffectStore struct {
	*Store
	clockMu sync.Mutex
	nowFn   func() time.Time
}

var (
	_ sideeffect.Store             = (*SideEffectStore)(nil)
	_ sideeffect.OwnedResolveStore = (*SideEffectStore)(nil)
)

// SideEffects 返回挂在同一个库上的副作用闸存储（零值可用；UseClock 可选）。
func (s *Store) SideEffects() *SideEffectStore { return &SideEffectStore{Store: s} }

// UseClock 注入时钟（sidetest 契约套件用）。nil 忽略。
func (s *SideEffectStore) UseClock(now func() time.Time) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	if now != nil {
		s.nowFn = now
	}
}

// now 判定时刻：注入时钟优先（契约套件），否则进程 UTC 时钟（与 leader_leases
// 「跨实例时钟偏移远小于 TTL」同口径）。必须在取得行锁之后调用。
func (s *SideEffectStore) now() time.Time {
	s.clockMu.Lock()
	fn := s.nowFn
	s.clockMu.Unlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

// inTx 单事务骨架；SQLite 下额外持进程写门（单写者语义）。
func (s *SideEffectStore) inTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(fn)
	})
}

// lockQuery MySQL/PG 行锁；SQLite 无 FOR UPDATE（由进程写门串行）。
func (s *SideEffectStore) lockQuery(tx *gorm.DB) *gorm.DB {
	if s.isSQLite() {
		return tx
	}
	return tx.Clauses(clause.Locking{Strength: "UPDATE"})
}

// ensureResourceRow 保证资源行存在（首个动作顺手建行；fence 从 0 起）。
func (s *SideEffectStore) ensureResourceRow(tx *gorm.DB, resource string) error {
	now := s.now()
	var err error
	switch s.dialect {
	case DialectMySQL:
		err = tx.Exec("INSERT IGNORE INTO side_effect_fences (resource, fence, holder, updated_at) VALUES (?, 0, '', ?)", resource, now).Error
	case DialectPostgres:
		err = tx.Exec("INSERT INTO side_effect_fences (resource, fence, holder, updated_at) VALUES (?, 0, '', ?) ON CONFLICT DO NOTHING", resource, now).Error
	default:
		err = tx.Exec("INSERT OR IGNORE INTO side_effect_fences (resource, fence, holder, updated_at) VALUES (?, 0, '', ?)", resource, now).Error
	}
	if err != nil {
		return fmt.Errorf("storage: 登记 side_effect 资源行失败: %w", err)
	}
	return nil
}

// ensureActionRow 保证 Key 行存在，并**在首次见面时登记动作身份**（Resource /
// Payload hash / RetrySafety）——即便本次判定的结果是 contended：身份不等授予。
func (s *SideEffectStore) ensureActionRow(tx *gorm.DB, act sideeffect.Action) error {
	now := s.now()
	args := []any{string(act.Key), act.Resource, sideEffectStatusPending,
		int16(act.RetrySafety), sideeffect.HashPayload(act.Payload), now, now}
	var err error
	switch s.dialect {
	case DialectMySQL:
		err = tx.Exec("INSERT IGNORE INTO side_effects (effect_key, resource, status, retry_safety, payload_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", args...).Error
	case DialectPostgres:
		err = tx.Exec("INSERT INTO side_effects (effect_key, resource, status, retry_safety, payload_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING", args...).Error
	default:
		err = tx.Exec("INSERT OR IGNORE INTO side_effects (effect_key, resource, status, retry_safety, payload_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)", args...).Error
	}
	if err != nil {
		return fmt.Errorf("storage: 登记 side_effect 动作行失败: %w", err)
	}
	return nil
}

// lockResource 锁并读出资源行（调用前必须 ensureResourceRow）。
func (s *SideEffectStore) lockResource(tx *gorm.DB, resource string) (*sideEffectFenceRow, error) {
	var r sideEffectFenceRow
	if err := s.lockQuery(tx).Where("resource = ?", resource).Take(&r).Error; err != nil {
		return nil, fmt.Errorf("storage: 读 side_effect 资源行失败: %w", err)
	}
	return &r, nil
}

// lockAction 锁住并读出 Key 行，**锁后立刻校验动作身份**（见 ensureIdentity）
// ——身份判定先于任何状态判定（已完成重放也不得把旧身份的结果交给新调用方）。
func (s *SideEffectStore) lockAction(tx *gorm.DB, act sideeffect.Action) (*sideEffectRow, error) {
	var a sideEffectRow
	if err := s.lockQuery(tx).Where("effect_key = ?", string(act.Key)).Take(&a).Error; err != nil {
		return nil, fmt.Errorf("storage: 读 side_effect 动作行失败: %w", err)
	}
	if err := s.ensureIdentity(tx, &a, act); err != nil {
		return nil, err
	}
	return &a, nil
}

// ensureIdentity 动作身份不可变：首次登记后 Resource/Payload/RetrySafety 不一致
// → ErrActionConflict，且不授予、不重放、不转未知态（判定优先于已完成重放）。
func (s *SideEffectStore) ensureIdentity(tx *gorm.DB, a *sideEffectRow, act sideeffect.Action) error {
	hash := sideeffect.HashPayload(act.Payload)
	if a.PayloadHash == "" || a.Resource == "" {
		// 老数据行兼容（不改变既有事实，只在身份缺失时补登记）。
		a.Resource = act.Resource
		a.RetrySafety = int16(act.RetrySafety)
		a.PayloadHash = hash
		return s.writeAction(tx, a, s.now())
	}
	if a.Resource != act.Resource || a.RetrySafety != int16(act.RetrySafety) || a.PayloadHash != hash {
		return fmt.Errorf("storage: 同 Key 的动作身份与首次登记不一致（不是重试，是另一个动作）: %w", sideeffect.ErrActionConflict)
	}
	return nil
}

// writeAction 写回 Key 级状态（调用方持行锁；map 更新允许写零值/NULL）。
func (s *SideEffectStore) writeAction(tx *gorm.DB, a *sideEffectRow, now time.Time) error {
	err := tx.Model(&sideEffectRow{}).Where("effect_key = ?", a.EffectKey).Updates(map[string]any{
		"resource":     a.Resource,
		"status":       a.Status,
		"retry_safety": a.RetrySafety,
		"payload_hash": a.PayloadHash,
		"holder":       a.Holder,
		"fence":        a.Fence,
		"lease_until":  a.LeaseUntil,
		"result":       a.Result,
		"reason":       a.Reason,
		"recon_holder": a.ReconHolder,
		"recon_until":  a.ReconUntil,
		"updated_at":   now,
	}).Error
	if err != nil {
		return fmt.Errorf("storage: 写 side_effect 动作行失败: %w", err)
	}
	return nil
}

// writeResource 写回资源级状态（调用方持行锁）。
func (s *SideEffectStore) writeResource(tx *gorm.DB, r *sideEffectFenceRow, now time.Time) error {
	err := tx.Model(&sideEffectFenceRow{}).Where("resource = ?", r.Resource).Updates(map[string]any{
		"fence":       r.Fence,
		"holder":      r.Holder,
		"lease_until": r.LeaseUntil,
		"updated_at":  now,
	}).Error
	if err != nil {
		return fmt.Errorf("storage: 写 side_effect 资源行失败: %w", err)
	}
	return nil
}

// lockPair 一次事务的行读取：资源行在前、动作行在后（锁序恒定）。
func (s *SideEffectStore) lockPair(tx *gorm.DB, act sideeffect.Action) (*sideEffectFenceRow, *sideEffectRow, error) {
	if err := s.ensureResourceRow(tx, act.Resource); err != nil {
		return nil, nil, err
	}
	if err := s.ensureActionRow(tx, act); err != nil {
		return nil, nil, err
	}
	rr, err := s.lockResource(tx, act.Resource)
	if err != nil {
		return nil, nil, err
	}
	a, err := s.lockAction(tx, act)
	if err != nil {
		return nil, nil, err
	}
	return rr, a, nil
}

// leaseResourceErr 三项联查之一：租约的资源必须与动作的资源一致。
func leaseResourceErr(act sideeffect.Action, lease sideeffect.Lease) error {
	if lease.Resource != act.Resource {
		return fmt.Errorf("storage: 租约与动作的资源不一致: %w", sideeffect.ErrResourceMismatch)
	}
	return nil
}

// holdOwnerErr 三项联查：资源闸是否仍由**这份凭据**持有（令牌最新代 + holder 一致）。
func holdOwnerErr(act sideeffect.Action, lease sideeffect.Lease, rr *sideEffectFenceRow) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if lease.Fence == 0 || rr.Holder == "" || rr.Fence != int64(lease.Fence) {
		return fmt.Errorf("storage: 本次持有已失效（令牌不是最新代或资源已释放）: %w", sideeffect.ErrFenceStale)
	}
	if rr.Holder != lease.Holder {
		return fmt.Errorf("storage: 持有者身份不匹配: %w", sideeffect.ErrNotHolder)
	}
	return nil
}

// intentOwnerErr 三项联查：本次尝试是否仍归**这份凭据**所有。
func intentOwnerErr(act sideeffect.Action, lease sideeffect.Lease, a *sideEffectRow) error {
	if err := leaseResourceErr(act, lease); err != nil {
		return err
	}
	if !a.hasIntent() {
		return fmt.Errorf("storage: 本次尝试已无未裁决的执行意图: %w", sideeffect.ErrFenceStale)
	}
	if *a.Fence != int64(lease.Fence) {
		return fmt.Errorf("storage: 本次尝试已不属于该令牌: %w", sideeffect.ErrFenceStale)
	}
	if a.Holder == nil || *a.Holder != lease.Holder {
		return fmt.Errorf("storage: 持有者身份不匹配（意图不是本次授予铸造的那份）: %w", sideeffect.ErrNotHolder)
	}
	return nil
}

// copyBytes 结果落库/取回都做一次拷贝（防调用方/驱动共享底层数组）。
func copyBytes(b []byte) []byte { return append([]byte(nil), b...) }

func i64ptr(v int64) *int64 { return &v }

// errInvalidAction 动作缺键或缺资源：显式拒绝，不静默放行。
func errInvalidAction() error {
	return fmt.Errorf("storage: 副作用动作缺键或缺资源: %w", sideeffect.ErrInvalidAction)
}
