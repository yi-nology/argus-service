// side_effects_acquire.go —— Acquire：五步判定的单事务实现。
//
// 判定顺序是契约里最要紧的一行：**已完成 > 结果未知 > 执行中/对账中 > 资源被占 > 授予**。
// 顺序写反的代价是重复施加：把「结果未知」排到「资源被占」之后，调用方会拿到
// contended（以为「晚点重试就行」），于是重试发生、重复施加随之发生。
package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// Acquire 一次原子判定（契约语义逐条对照 sideeffect.Store.Acquire）。
//
// 授予时在同一事务里做三件事：令牌 = 资源已见最大值 + 1；持有者身份由
// sideeffect.MintHolder() 铸造；执行意图（令牌 + 持有者 + 到期时刻）随授予落库。
// 三者分开写就等于没写：中间死掉 ⇒ 意图悬空或没写，重复施加的入口重新打开。
func (s *SideEffectStore) Acquire(ctx context.Context, act sideeffect.Action, ttl time.Duration) (sideeffect.Outcome, sideeffect.Lease, sideeffect.Result, error) {
	if act.Key == "" || act.Resource == "" {
		return sideeffect.OutcomeContended, sideeffect.Lease{}, sideeffect.Result{}, errInvalidAction()
	}
	var (
		outcome sideeffect.Outcome
		lease   sideeffect.Lease
		res     sideeffect.Result
	)
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		rr, a, err := s.lockPair(tx, act)
		if err != nil {
			return err
		}
		now := s.now()

		// 1. 已完成 → 重放既有结果（不再需要令牌；优先于资源占用）。
		if a.Status == sideEffectStatusDone {
			outcome, res = sideeffect.OutcomeReplayed, sideeffect.Result{Payload: copyBytes(a.Result)}
			return nil
		}
		// 2. 结果未知 → 硬阻塞（先对账，不是等一等）。
		if a.Status == sideEffectStatusUncertain {
			outcome = sideeffect.OutcomeUncertain
			return nil
		}
		// 2b. 对账占用未过期 ⇒ 对账中：仍属未知态一族。过期的占用在这里清掉
		// （可重入：对账者死亡不永久占住这个 Key）。
		if a.ReconUntil != nil {
			if now.Before(*a.ReconUntil) {
				outcome = sideeffect.OutcomeUncertain
				return nil
			}
			a.ReconHolder, a.ReconUntil = nil, nil
			if err := s.writeAction(tx, a, now); err != nil {
				return err
			}
		}
		// 3. 执行中：这次尝试还没裁决 ⇒ 不得重新授予执行权。
		if a.hasIntent() {
			if a.LeaseUntil != nil && now.Before(*a.LeaseUntil) {
				outcome = sideeffect.OutcomeContended
				return nil
			}
			// 租约过期 ⇒ 这次尝试的结果未知（可能已施加、账没记上）。
			intentHolder := ""
			if a.Holder != nil {
				intentHolder = *a.Holder
			}
			intentFence := *a.Fence
			a.Holder, a.Fence, a.LeaseUntil = nil, nil, nil
			// 意图与资源持有是同一次尝试的两个投影：作废意图时，若资源闸仍是
			// 这次尝试的那份，一并作废（没人接管时令牌没变，不清的话
			// 「尝试已转入未知态」在资源侧看不出来）。
			if rr.Holder != "" && rr.Holder == intentHolder && rr.Fence == intentFence {
				rr.Holder, rr.LeaseUntil = "", nil
				if err := s.writeResource(tx, rr, now); err != nil {
					return err
				}
			}
			if act.RetrySafety != sideeffect.RetrySafetySafe {
				a.Status = sideEffectStatusUncertain
				a.Reason = "上一次尝试的租约已过期，结果未知"
				if err := s.writeAction(tx, a, now); err != nil {
					return err
				}
				outcome = sideeffect.OutcomeUncertain
				return nil
			}
		}
		// 4. 资源被他人持有且租约未过期 → 可重试的拒绝。
		if rr.held(now) {
			outcome = sideeffect.OutcomeContended
			return nil
		}
		// 5. 授予：令牌单调递增 + 铸造持有者身份 + 登记执行意图，同一事务。
		rr.Fence++
		holder := sideeffect.MintHolder()
		until := now.Add(ttl)
		rr.Holder, rr.LeaseUntil = holder, &until
		if err := s.writeResource(tx, rr, now); err != nil {
			return err
		}
		a.Status = sideEffectStatusPending
		a.Holder, a.Fence, a.LeaseUntil = &holder, i64ptr(rr.Fence), &until
		if err := s.writeAction(tx, a, now); err != nil {
			return err
		}
		outcome = sideeffect.OutcomeApplied
		lease = sideeffect.Lease{Resource: act.Resource, Fence: sideeffect.Fence(rr.Fence), Holder: holder, TTL: ttl}
		return nil
	})
	return outcome, lease, res, err
}
