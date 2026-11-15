// side_effects_reconcile.go —— 对账侧：ResolveUncertain / ResolveUncertainOwned /
// BeginReconcile。
//
// 裁决**不校验栅栏**（契约硬要求）：它的依据是下游事实，不是持有权——原持有者
// 可能已经死亡、租约早已过期，而对账恰恰要在这种情况下进行。占用与裁决的关系：
// 占用过期可重入（对账者死亡不永久占住）；裁决成功即清除占用。
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

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
		if a.Status == sideEffectStatusDone {
			outcome, out = sideeffect.OutcomeReplayed, sideeffect.Result{Payload: copyBytes(a.Result)}
			return nil
		}
		now := s.now()
		if requiredHolder != "" {
			if a.ReconUntil == nil || a.ReconHolder == nil || !now.Before(*a.ReconUntil) || *a.ReconHolder != requiredHolder {
				return fmt.Errorf("storage: 出示的对账认领不是当前有效的一份（已过期或已被接管）: %w", sideeffect.ErrReconcileLost)
			}
		}
		if a.Status != sideEffectStatusUncertain {
			return fmt.Errorf("storage: 该 Key 不处于结果未知态: %w", sideeffect.ErrNotUncertain)
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
				return err
			}
			outcome, out = sideeffect.OutcomeReplayed, sideeffect.Result{Payload: payload}
			return nil
		}
		if act.RetrySafety == sideeffect.RetrySafetyUnsafe && !res.ForceRetryable {
			// 「不可重试」的动作，一句来自对账的「未生效」不足以重新开门：
			// 未知标记原样保留，放行必须显式签字。
			if err := s.writeAction(tx, a, now); err != nil {
				return err
			}
			outcome = sideeffect.OutcomeUncertain
			return fmt.Errorf("storage: 动作声明为不可重试：未生效的裁决不解除阻塞: %w", sideeffect.ErrUnsafeNotRetryable)
		}
		a.Status = sideEffectStatusPending
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		outcome = sideeffect.OutcomeApplied
		return nil
	})
	return outcome, out, err
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
