// side_effects_admin.go —— 控制面人工介入的存储面（#48）：活跃资源租约清单 +
// 强制释放。与凭据一族（side_effects_lease.go）的区别：这里不校验 fence/holder
// 凭据——操作员处置的是「持有者可能已死/失联」的现场，依据是人工判断而非持有权。
//
// 强制释放的边界（刻意不做的事）：
//   - 不动 side_effects.status/result：done 不复活、uncertain 仍由 reconciler /
//     人工裁决处理——释放只是把「执行意图」与「资源闸」清空，不是替人裁决；
//   - 不动资源行 fence：令牌代际继续单调，旧持有者恢复后拿旧令牌写入仍被下游拒。
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// 活跃租约清单的批量口径：缺省 50，上限 200（控制面观测面，防单页扫全表）。
const (
	defaultActiveLeaseLimit = 50
	maxActiveLeaseLimit     = 200
)

// SideEffectLease 一条活跃 side-effect 资源租约（ListActiveLeases 的行投影）。
type SideEffectLease struct {
	Resource   string     `gorm:"column:resource"`
	Holder     string     `gorm:"column:holder"`
	Fence      int64      `gorm:"column:fence"`
	LeaseUntil *time.Time `gorm:"column:lease_until"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

// ListActiveLeases 列出当前活跃（holder 非空且 lease_until 未过期）的资源租约，
// 按到期时间升序（最快过期的先被看见）。limit<=0 取 50，最大 200。只读。
func (s *SideEffectStore) ListActiveLeases(ctx context.Context, limit int) ([]SideEffectLease, error) {
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
func (s *SideEffectStore) ForceReleaseResource(ctx context.Context, resource string) (clearedActions int64, err error) {
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
