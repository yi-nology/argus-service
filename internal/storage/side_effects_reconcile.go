// side_effects_reconcile.go —— 对账侧：ResolveUncertain / ResolveUncertainOwned /
// BeginReconcile。
//
// 裁决**不校验栅栏**（契约硬要求）：它的依据是下游事实，不是持有权——原持有者
// 可能已经死亡、租约早已过期，而对账恰恰要在这种情况下进行。占用与裁决的关系：
// 占用过期可重入（对账者死亡不永久占住）；裁决成功即清除占用。
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// defaultUncertainListLimit ListUncertain 未显式指定 limit 时的批量上界。
const defaultUncertainListLimit = 50

// UncertainSideEffect 一条待对账的未知态 Key（ListUncertain 的行投影）。
// 只带对账需要的判据，不向调用方暴露 holder/fence 等执行意图细节。
type UncertainSideEffect struct {
	Key         string    `gorm:"column:effect_key"`
	Resource    string    `gorm:"column:resource"`
	PayloadHash string    `gorm:"column:payload_hash"`
	RetrySafety int16     `gorm:"column:retry_safety"`
	Reason      string    `gorm:"column:reason"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

// ListUncertain 列出 updated_at 早于 olderThan 的未知态 Key（按龄升序，最老的
// 先对账）。limit<=0 取 50：不设上界会让单轮对账把全表未知态一次扫完，饿死
// 其余周期任务。只读，不加写门。
func (s *SideEffectStore) ListUncertain(ctx context.Context, olderThan time.Time, limit int) ([]UncertainSideEffect, error) {
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
func (s *SideEffectStore) ResolveUncertainKey(ctx context.Context, key string, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
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

// ResolveUncertain 裁决「结果未知」的 Key。**先到者为准**（简化入口，不校验
// 对账凭据——短对账无须认领，见 OwnedResolveStore）。
func (s *SideEffectStore) ResolveUncertain(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution) (sideeffect.Outcome, sideeffect.Result, error) {
	return s.resolve(ctx, act, res, "")
}

// ResolveUncertainOwned 带对账占用凭据的裁决：holder 不是**当前有效**对账认领
// 的持有者 → ErrReconcileLost，不改任何状态。
func (s *SideEffectStore) ResolveUncertainOwned(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution, holder string) (sideeffect.Outcome, sideeffect.Result, error) {
	if holder == "" {
		return sideeffect.OutcomeContended, sideeffect.Result{},
			fmt.Errorf("storage: 空凭据不是任何一次对账认领: %w", sideeffect.ErrReconcileLost)
	}
	return s.resolve(ctx, act, res, holder)
}

// resolve 裁决主体（事务内、行已锁）。requiredHolder 非空时先校验对账占用凭据；
// 已完成的重放无害，先于凭据校验（读既有事实不问资格）。
func (s *SideEffectStore) resolve(ctx context.Context, act sideeffect.Action, res sideeffect.Resolution, requiredHolder string) (sideeffect.Outcome, sideeffect.Result, error) {
	var (
		outcome sideeffect.Outcome
		out     sideeffect.Result
	)
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		if err := s.ensureActionRow(tx, act); err != nil {
			return err
		}
		a, err := s.lockAction(tx, act)
		if err != nil {
			return err
		}
		now := s.now()
		// 已完成的重放不校验凭据（读既有事实不问资格）。
		if a.Status != sideEffectStatusDone && requiredHolder != "" {
			if a.ReconUntil == nil || a.ReconHolder == nil || !now.Before(*a.ReconUntil) || *a.ReconHolder != requiredHolder {
				return fmt.Errorf("storage: 出示的对账认领不是当前有效的一份（已过期或已被接管）: %w", sideeffect.ErrReconcileLost)
			}
		}
		outcome, out, err = s.applyResolution(tx, a, res, now)
		return err
	})
	return outcome, out, err
}

// applyResolution 裁决已锁定的 Key 行：resolve 与 ResolveUncertainKey 共用这一
// 份状态语义（已完成→重放；非未知态→ErrNotUncertain；已生效→done；未生效→
// pending，Unsafe 动作需要 ForceRetryable 签字）。调用方负责事务/行锁/凭据校验。
func (s *SideEffectStore) applyResolution(tx *gorm.DB, a *sideEffectRow, res sideeffect.Resolution, now time.Time) (sideeffect.Outcome, sideeffect.Result, error) {
	if a.Status == sideEffectStatusDone {
		return sideeffect.OutcomeReplayed, sideeffect.Result{Payload: copyBytes(a.Result)}, nil
	}
	if a.Status != sideEffectStatusUncertain {
		return sideeffect.OutcomeContended, sideeffect.Result{},
			fmt.Errorf("storage: 该 Key 不处于结果未知态: %w", sideeffect.ErrNotUncertain)
	}
	// 裁决即结论：执行意图与对账占用一并撤销。
	a.Holder, a.Fence, a.LeaseUntil = nil, nil, nil
	a.ReconHolder, a.ReconUntil = nil, nil
	if res.Applied {
		payload := copyBytes(res.Result.Payload)
		a.Status = sideEffectStatusDone
		a.Result = payload
		a.Reason = res.Reason
		if err := s.writeAction(tx, a, now); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Result{}, err
		}
		return sideeffect.OutcomeReplayed, sideeffect.Result{Payload: payload}, nil
	}
	if sideeffect.RetrySafety(a.RetrySafety) == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable {
		// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门：
		// 未知标记原样保留，放行必须显式签字。
		if err := s.writeAction(tx, a, now); err != nil {
			return sideeffect.OutcomeContended, sideeffect.Result{}, err
		}
		return sideeffect.OutcomeUncertain, sideeffect.Result{},
			fmt.Errorf("storage: 动作声明为不可重试：未生效的裁决不解除阻塞: %w", sideeffect.ErrUnsafeNotRetryable)
	}
	a.Status = sideEffectStatusPending
	if err := s.writeAction(tx, a, now); err != nil {
		return sideeffect.OutcomeContended, sideeffect.Result{}, err
	}
	return sideeffect.OutcomeApplied, sideeffect.Result{}, nil
}

// BeginReconcile 认领对账权：同一 Key 同一时刻至多一个对账者。
//
// 仅当该 Key 处于未知态时接受认领；占用未过期时拒绝（ErrReconcileTaken）；
// 占用过期可重新认领（可重入）——对账者死亡不永久占住这个 Key。
func (s *SideEffectStore) BeginReconcile(ctx context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Reconcile, error) {
	if ttl <= 0 {
		return sideeffect.Reconcile{}, fmt.Errorf("storage: 对账认领时长必须为正: %w", sideeffect.ErrInvalidTTL)
	}
	var out sideeffect.Reconcile
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		if err := s.ensureActionRow(tx, act); err != nil {
			return err
		}
		a, err := s.lockAction(tx, act)
		if err != nil {
			return err
		}
		now := s.now()
		if a.Status != sideEffectStatusUncertain {
			return fmt.Errorf("storage: 该 Key 不处于结果未知态: %w", sideeffect.ErrNotUncertain)
		}
		if a.ReconUntil != nil && now.Before(*a.ReconUntil) {
			return fmt.Errorf("storage: 该 Key 已有对账占用: %w", sideeffect.ErrReconcileTaken)
		}
		holder := sideeffect.MintHolder()
		expires := now.Add(ttl)
		a.ReconHolder, a.ReconUntil = &holder, &expires
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		out = sideeffect.Reconcile{Holder: holder, ExpiresAt: expires}
		return nil
	})
	return out, err
}
