// side_effects_lease.go —— 凭据一族：Complete / Release / Abandon / Renew / MarkUncertain。
//
// 五个方法共用同一套纪律：
//   - 校验与写入在同一事务里 ⇒ 校验失败即整体回滚，不剩半截状态；
//   - 资源行 → 动作行的固定锁序；
//   - 三项联查不过即拒（ErrFenceStale / ErrNotHolder / ErrResourceMismatch），
//     拒绝时不落任何一行。
package storage

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// Complete 标记该 Key 的动作已完成并留存结果。**先到者为准**（幂等：后到者
// 不覆盖首次结果）。完成后资源闸释放，否则后来者会被「资源正忙」挡住。
func (s *SideEffectStore) Complete(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, res sideeffect.Result) (sideeffect.Result, error) {
	var out sideeffect.Result
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		rr, a, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		if a.Status == sideEffectStatusDone {
			// 已完成的既有事实不因出示者过期而不可见（先到者为准）。
			out = sideeffect.Result{Payload: copyBytes(a.Result)}
			return nil
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return err
		}
		payload := copyBytes(res.Payload)
		a.Status = sideEffectStatusDone
		a.Result = payload
		a.Reason = ""
		a.Holder, a.Fence, a.LeaseUntil = nil, nil, nil
		now := s.now()
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		rr.Holder, rr.LeaseUntil = "", nil
		if err := s.writeResource(tx, rr, now); err != nil {
			return err
		}
		out = sideeffect.Result{Payload: payload}
		return nil
	})
	return out, err
}

// Release 释放**资源持有**（闸），**不动**该 Key 的执行意图。
//
// 意图没被裁决之前，这个 Key 不得再被执行——两条出口（Abandon / Release）
// 不可互相替代，见 sideeffect.Store.Release 的契约注释。
func (s *SideEffectStore) Release(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease) error {
	return s.inTx(ctx, func(tx *gorm.DB) error {
		rr, _, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return err
		}
		rr.Holder, rr.LeaseUntil = "", nil
		return s.writeResource(tx, rr, s.now())
	})
}

// Abandon 显式放弃本次尝试：撤销执行意图，并释放（仍是自己的）资源持有。
//
// 资源闸只在仍是自己的时才放——易主了就不碰（那是新持有者的闸）。
func (s *SideEffectStore) Abandon(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, _ string) error {
	return s.inTx(ctx, func(tx *gorm.DB) error {
		rr, a, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return err
		}
		a.Holder, a.Fence, a.LeaseUntil = nil, nil, nil
		now := s.now()
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		if holdOwnerErr(act, lease, rr) == nil {
			rr.Holder, rr.LeaseUntil = "", nil
			if err := s.writeResource(tx, rr, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// Renew 续期并**同时确认持有资格**：闸已易主、尝试已失效**或租约已过期**
// 则拒绝续期（假续期是重复施加的入口）。续期不换令牌（下游已认这一代）。
func (s *SideEffectStore) Renew(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, ttl time.Duration) (sideeffect.Lease, error) {
	var out sideeffect.Lease
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		rr, a, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return err
		}
		now := s.now()
		// 过期即资格终止：租约已过期而无人接管时，旧持有者不得复活它。
		if rr.LeaseUntil == nil || !rr.LeaseUntil.After(now) {
			return fmt.Errorf("storage: 租约已过期且无人接管——到期即资格终止（要继续做请重新 Acquire）: %w", sideeffect.ErrFenceStale)
		}
		until := now.Add(ttl)
		rr.LeaseUntil = &until
		if err := s.writeResource(tx, rr, now); err != nil {
			return err
		}
		a.LeaseUntil = &until
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		out = sideeffect.Lease{Resource: act.Resource, Fence: lease.Fence, Holder: lease.Holder, TTL: ttl}
		return nil
	})
	return out, err
}

// MarkUncertain 把该 Key 标记为「结果未知」（按 Key，与资源租约无关）。
//
// 校验与写入同事务：标记写失败时整体回滚 ⇒ 执行意图原样留在存储里，后续
// Acquire 被意图挡住。标记成功后意图一并撤销，但闸不自动释放——调用方还要
// 显式 Release 只放资源。
func (s *SideEffectStore) MarkUncertain(ctx context.Context, act sideeffect.Action, lease sideeffect.Lease, reason string) error {
	return s.inTx(ctx, func(tx *gorm.DB) error {
		rr, a, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		if err := holdOwnerErr(act, lease, rr); err != nil {
			return err
		}
		if err := intentOwnerErr(act, lease, a); err != nil {
			return err
		}
		a.Status = sideEffectStatusUncertain
		a.Reason = reason
		a.Holder, a.Fence, a.LeaseUntil = nil, nil, nil
		return s.writeAction(tx, a, s.now())
	})
}
