// side_effects_admin_store.go —— 副作用**处置面**存储（#60 拆分，roadmap §6.2
// 按职责拆）：控制面人工介入（清单/裁决/强释/授权读）与自动对账循环的入口。
// 与 SideEffectStore（闸核心：Acquire/Renew/Release/Complete 等执行语义，实现
// kernel sideeffect.Store 契约）分立——两者的变化原因不同：闸语义跟 kernel
// 契约走，处置面跟运维/授权需求走。
// 继承自 side_effects_admin.go 的处置边界（刻意不做的事）：
//   - 不校验 fence/holder 凭据——操作员处置的是「持有者可能已死/失联」的现场，
//     依据是人工判断而非持有权；
//   - 强释不动 side_effects.status/result：done 不复活、uncertain 仍由 reconciler/
//     人工裁决处理——释放只是把「执行意图」与「资源闸」清空，不是替人裁决；
//   - 不动资源行 fence：令牌代际继续单调，旧持有者恢复后拿旧令牌写入仍被下游拒。

package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// SideEffectAdminStore 副作用处置面：uncertain 清单/按 Key 裁决/活跃租约/
// 强制释放/控制面授权读。包装同一底层闸核心（事务/锁/裁决主体共用其实现）。
// SideEffectLease 一条活跃 side-effect 资源租约（ListActiveLeases 的行投影）。
type SideEffectLease struct {
	Resource   string     `gorm:"column:resource"`
	Holder     string     `gorm:"column:holder"`
	Fence      int64      `gorm:"column:fence"`
	LeaseUntil *time.Time `gorm:"column:lease_until"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

// 活跃租约清单的批量口径：缺省 50，上限 200（控制面观测面，防单页扫全表）。
const (
	defaultActiveLeaseLimit = 50
	maxActiveLeaseLimit     = 200
)

type SideEffectAdminStore struct {
	core *SideEffectStore
}

// SideEffectAdmin 处置面装配入口（与 SideEffects() 同源同库）。
func (s *Store) SideEffectAdmin() *SideEffectAdminStore {
	return &SideEffectAdminStore{core: s.SideEffects()}
}

// ListUncertain 列出 updated_at 早于 olderThan 的未知态 Key（按龄升序，最老的
// 先对账）。limit<=0 取 50：不设上界会让单轮对账把全表未知态一次扫完，饿死
// 其余周期任务。只读，不加写门。
func (a *SideEffectAdminStore) ListUncertain(ctx context.Context, olderThan time.Time, limit int) ([]UncertainSideEffect, error) {
	s := a.core
	if limit <= 0 {
		limit = defaultUncertainListLimit
	}
	var rows []UncertainSideEffect
	err := s.db.WithContext(ctx).
		Model(&sideEffectRow{}).
		Select("effect_key", "resource", "payload_hash", "retry_safety", "reason", "updated_at").
		Where("status = ? AND updated_at < ?", sideEffectStatusUncertain, olderThan).
		Order("updated_at ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("storage: 列出未知态 side_effect 失败: %w", err)
	}
	return rows, nil
}

// ResolveUncertainKey 按 Key 裁决「结果未知」的动作（宿主扩展：行式对账/人工
// 裁决 API 入口）。与 ResolveUncertain 的区别是**身份已在行里**：不做 Action
// 身份校验，也不创建动作行——不存在的 Key 没有可裁决的未知态。
//
// 状态校验不绕过：只有 uncertain 接受裁决；已 done 按重放返回既有结果；其余
// 状态（pending/不存在）一律 ErrNotUncertain。裁决语义与 resolve 共用
// applyResolution，避免两套判定漂移。
func (a *SideEffectAdminStore) ResolveUncertainKey(ctx context.Context, key string, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	s := a.core
	if key == "" {
		return sideeffect.OutcomeContended, sideeffect.Result{},
			fmt.Errorf("storage: 空 Key 不处于结果未知态: %w", sideeffect.ErrNotUncertain)
	}
	var (
		outcome sideeffect.Outcome
		out     sideeffect.Result
	)
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		var a sideEffectRow
		qerr := s.lockQuery(tx).Where("effect_key = ?", key).Take(&a).Error
		if errors.Is(qerr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("storage: Key %q 不存在，无未知态可裁决: %w", key, sideeffect.ErrNotUncertain)
		}
		if qerr != nil {
			return fmt.Errorf("storage: 读 side_effect 动作行失败: %w", qerr)
		}
		var err error
		outcome, out, err = s.applyResolution(tx, &a, res, s.now())
		return err
	})
	return outcome, out, err
}

// ListActiveLeases 列出当前活跃（holder 非空且 lease_until 未过期）的资源租约，
// 按到期时间升序（最快过期的先被看见）。limit<=0 取 50，最大 200。只读。
func (a *SideEffectAdminStore) ListActiveLeases(ctx context.Context, limit int) ([]SideEffectLease, error) {
	s := a.core
	if limit <= 0 {
		limit = defaultActiveLeaseLimit
	}
	if limit > maxActiveLeaseLimit {
		limit = maxActiveLeaseLimit
	}
	now := s.now()
	var rows []SideEffectLease
	err := s.db.WithContext(ctx).
		Model(&sideEffectFenceRow{}).
		Select("resource", "holder", "fence", "lease_until", "updated_at").
		Where("holder <> '' AND lease_until > ?", now).
		Order("lease_until ASC").
		Limit(limit).
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("storage: 列出活跃 side_effect 租约失败: %w", err)
	}
	return rows, nil
}

// ForceReleaseResource 强制释放一个资源的租约，并清掉该资源下所有未裁决的执行
// 意图（holder/fence/lease_until/recon_holder/recon_until 一并置 NULL）。
//
// 单事务，锁序仍是资源行先、动作行后（与 Acquire/Release 一族一致）。资源行
// 不存在返回 ErrNotFound（先 Acquire 才会登记资源，没登记过就没有可释放的租约）。
//
// status/result 一律不动：释放是「撤回持有权」，不是「对下游事实的裁决」。
// 用户手工释放属审计操作，token 指纹留痕在 service 层，存储层不感知调用者身份。
func (a *SideEffectAdminStore) ForceReleaseResource(ctx context.Context, resource string) (clearedActions int64, err error) {
	s := a.core
	err = s.inTx(ctx, func(tx *gorm.DB) error {
		var rr sideEffectFenceRow
		qerr := s.lockQuery(tx).Where("resource = ?", resource).Take(&rr).Error
		if errors.Is(qerr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("storage: side_effect 资源 %q 不存在: %w", resource, ErrNotFound)
		}
		if qerr != nil {
			return fmt.Errorf("storage: 读 side_effect 资源行失败: %w", qerr)
		}
		now := s.now()
		// 资源行：只清持有状态，fence 保持（令牌代际继续单调）。
		if err := tx.Model(&sideEffectFenceRow{}).Where("resource = ?", resource).
			Updates(map[string]any{"holder": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("storage: 释放 side_effect 资源行失败: %w", err)
		}
		// 动作行：只清执行意图/对账占用，status/result 原样保留。
		res := tx.Model(&sideEffectRow{}).
			Where("resource = ? AND (holder IS NOT NULL OR fence IS NOT NULL OR lease_until IS NOT NULL OR recon_holder IS NOT NULL OR recon_until IS NOT NULL)", resource).
			Updates(map[string]any{
				"holder":       nil,
				"fence":        nil,
				"lease_until":  nil,
				"recon_holder": nil,
				"recon_until":  nil,
				"updated_at":   now,
			})
		if res.Error != nil {
			return fmt.Errorf("storage: 清 side_effect 执行意图失败: %w", res.Error)
		}
		clearedActions = res.RowsAffected
		return nil
	})
	return clearedActions, err
}

// GetEffectByKeyForAuthz 按 Key 取副作用行（#60 控制面裁决前的授权判定）：
// 任意状态可取——授权与状态正交，状态冲突（非 uncertain）仍由 ResolveUncertainKey
// 判 409。不存在返回 ErrNotFound（调用方落既有 409 契约，不泄露 Key 存在性差异）。
func (a *SideEffectAdminStore) GetEffectByKeyForAuthz(ctx context.Context, key string) (UncertainSideEffect, error) {
	s := a.core
	var row UncertainSideEffect
	err := s.db.WithContext(ctx).
		Model(&sideEffectRow{}).
		Select("effect_key", "resource", "payload_hash", "retry_safety", "reason", "updated_at").
		Where("effect_key = ?", key).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, fmt.Errorf("storage: side_effect %q 不存在: %w", key, ErrNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("storage: 读 side_effect 行失败: %w", err)
	}
	return row, nil
}

// GetLeaseByResourceForAuthz 按资源取租约行（#60 强制释放前的授权判定）：
// 不要求活跃——404/释放语义由 ForceReleaseResource 承担，这里只服务「这个
// 资源属于哪个仓库」的归属判定。
func (a *SideEffectAdminStore) GetLeaseByResourceForAuthz(ctx context.Context, resource string) (SideEffectLease, error) {
	s := a.core
	var row SideEffectLease
	err := s.db.WithContext(ctx).
		Model(&sideEffectFenceRow{}).
		Select("resource", "holder", "fence", "lease_until", "updated_at").
		Where("resource = ?", resource).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, fmt.Errorf("storage: side_effect 资源 %q 不存在: %w", resource, ErrNotFound)
	}
	if err != nil {
		return row, fmt.Errorf("storage: 读 side_effect 资源行失败: %w", err)
	}
	return row, nil
}
