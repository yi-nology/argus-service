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
	"strings"

	"context"
	"errors"
	"fmt"
	"github.com/yi-nology/argus/internal/tenant"
	"time"

	"gorm.io/gorm"

	"github.com/oagentkit/goagent-kernel/sideeffect"
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
//
// olderThan 归一 UTC（SQL 边界单点）：行时刻是 UTC 时钟（SideEffectStore.now），
// SQLite 方言存文本按字典序比、MySQL/PG 列为无时区墙钟——本地时区截止串与
// UTC 存储串混比会错位（CI TZ=UTC 与本地 UTC+8 结果曾相反，测试侥幸绿抓获）。
func (a *SideEffectAdminStore) ListUncertain(ctx context.Context, olderThan time.Time, limit int) ([]UncertainSideEffect, error) {
	s := a.core
	if limit <= 0 {
		limit = defaultUncertainListLimit
	}
	var rows []UncertainSideEffect
	err := s.db.WithContext(ctx).
		Model(&sideEffectRow{}).
		Select("effect_key", "resource", "payload_hash", "payload", "retry_safety", "reason", "updated_at").
		Where("status = ? AND updated_at < ?", sideEffectStatusUncertain, olderThan.UTC()).
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

// ForceReleaseAction 强释预估清单里的一行（受影响 Action Key 的投影）。
type ForceReleaseAction struct {
	Key         string     `gorm:"column:effect_key"`
	Resource    string     `gorm:"column:resource"`
	Holder      *string    `gorm:"column:holder"`
	Fence       *int64     `gorm:"column:fence"`
	Status      string     `gorm:"column:status"`
	RetrySafety int16      `gorm:"column:retry_safety"`
	Reason      string     `gorm:"column:reason"`
	UpdatedAt   time.Time  `gorm:"column:updated_at"`
	LeaseUntil  *time.Time `gorm:"column:lease_until"`
}

// ForceReleasePlan 一次强制释放将影响的现场（dry-run 返回值；只读预估）。
type ForceReleasePlan struct {
	Resource   string
	Holder     string
	Fence      int64
	LeaseUntil *time.Time
	// Actions 受影响的动作行（挂着执行意图或对账占用的 Key），按 Key 升序。
	Actions []ForceReleaseAction
	// WouldMarkUncertain 其中 RetrySafetyUnsafe 且带在飞执行意图的 pending 行数：
	// 正式释放时这些 Key 会转入 uncertain，等待下游事实核验（#64 协议）。
	WouldMarkUncertain int64
}

// ForceReleaseInput 正式释放的操作凭据（审计面；存储层不解析身份，指纹原样落行）。
type ForceReleaseInput struct {
	// OperatorFP 操作员 token 指纹（sha256 前 8 字节 hex；空 = 内部/测试调用）。
	OperatorFP string
	// Reason 操作理由（控制面必填项；持久化进审计行）。
	Reason string
}

// ForceReleaseResult 正式释放的事实结果。
type ForceReleaseResult struct {
	// ClearedActions 一并清掉执行意图/对账占用的 action 行数（与旧口径一致）。
	ClearedActions int64
	// MarkedUncertain 其中 Unsafe + 在飞意图被转入 uncertain 的行数。
	MarkedUncertain int64
	// AuditID 持久化审计行 ID（同事务写入；取证/回看的锚点）。
	AuditID int64
}

// releaseAffectedWhere 受影响动作行的判定：挂着执行意图或对账占用的行。
const releaseAffectedWhere = "resource = ? AND (holder IS NOT NULL OR fence IS NOT NULL OR lease_until IS NOT NULL OR recon_holder IS NOT NULL OR recon_until IS NOT NULL)"

// DryRunForceRelease 强释预演（#64）：列出正式释放将影响的一切——资源行现场、
// 受影响 Action 清单（Key/holder/fence/状态/重试安全性）、将转入 uncertain 的
// 行数——**不修改任何行**（纯读，不加写锁）。资源不存在返回 ErrNotFound。
func (a *SideEffectAdminStore) DryRunForceRelease(ctx context.Context, resource string) (ForceReleasePlan, error) {
	s := a.core
	var plan ForceReleasePlan
	plan.Resource = resource
	var rr sideEffectFenceRow
	err := s.db.WithContext(ctx).Model(&sideEffectFenceRow{}).
		Select("resource", "holder", "fence", "lease_until").
		Where("resource = ?", resource).Take(&rr).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return plan, fmt.Errorf("storage: side_effect 资源 %q 不存在: %w", resource, ErrNotFound)
	}
	if err != nil {
		return plan, fmt.Errorf("storage: 读 side_effect 资源行失败: %w", err)
	}
	plan.Holder, plan.Fence, plan.LeaseUntil = rr.Holder, rr.Fence, rr.LeaseUntil
	if err := s.db.WithContext(ctx).Model(&sideEffectRow{}).
		Select("effect_key", "resource", "holder", "fence", "status", "retry_safety", "reason", "updated_at", "lease_until").
		Where(releaseAffectedWhere, resource).
		Order("effect_key ASC").
		Scan(&plan.Actions).Error; err != nil {
		return plan, fmt.Errorf("storage: 列出受影响 side_effect 动作行失败: %w", err)
	}
	for _, act := range plan.Actions {
		if s.wouldMarkUncertain(act) {
			plan.WouldMarkUncertain++
		}
	}
	return plan, nil
}

// wouldMarkUncertain 正式释放时该动作行是否会转入 uncertain（#64 协议）：
// pending（带在飞意图的必然是 pending——MarkUncertain 撤意图、done 清意图）+
// RetrySafetyUnsafe。Unknown/Safe 的释放后语义不变（原样可重试）。
func (s *SideEffectStore) wouldMarkUncertain(act ForceReleaseAction) bool {
	return act.Status == sideEffectStatusPending &&
		sideeffect.RetrySafety(act.RetrySafety) == sideeffect.RetrySafetyUnsafe
}

// ForceReleaseResource 强制释放一个资源的租约，并清掉该资源下所有未裁决的执行
// 意图（holder/fence/lease_until/recon_holder/recon_until 一并置 NULL）。
//
// 单事务，锁序仍是资源行先、动作行后（与 Acquire/Release 一族一致）。资源行
// 不存在返回 ErrNotFound（先 Acquire 才会登记资源，没登记过就没有可释放的租约）。
//
// #64 协议（对第三方 API 的未知态兜底）：
//   - **Unsafe + 在飞意图的 pending 行默认转入 uncertain**（带 forced release
//     理由留痕），而不是只凭租约释放允许重试——Git Push / 创建 PR / 发评论没有
//     fence 概念，旧执行者的外部请求可能仍在途；这类 Key 必须等下游事实核验
//     （resolver 自动对账）或人工 force_retryable 才重新开门。Safe/Unknown 的
//     行保持原语义（释放后可重试，下游有幂等键/身份校验兜底）。
//   - **持久化审计**：审计行与释放同事务落库（side_effect_release_audits），
//     释放发生则审计必在；dry-run（DryRunForceRelease）不落行。
//
// status/result 的其余边界不变：done 不复活、已 uncertain 的行不重复裁决
// （只清对账占用）。资源行的 fence 保持单调（令牌代际不回退）。
func (a *SideEffectAdminStore) ForceReleaseResource(ctx context.Context, resource string, in ForceReleaseInput) (ForceReleaseResult, error) {
	s := a.core
	var out ForceReleaseResult
	err := s.inTx(ctx, func(tx *gorm.DB) error {
		var rr sideEffectFenceRow
		qerr := s.lockQuery(tx).Where("resource = ?", resource).Take(&rr).Error
		if errors.Is(qerr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("storage: side_effect 资源 %q 不存在: %w", resource, ErrNotFound)
		}
		if qerr != nil {
			return fmt.Errorf("storage: 读 side_effect 资源行失败: %w", qerr)
		}
		now := s.now()
		// 受影响动作行先读后写（行锁在手，读到的就是将改的）。
		var affected []sideEffectRow
		if err := s.lockQuery(tx).Where(releaseAffectedWhere, resource).
			Order("effect_key ASC").Find(&affected).Error; err != nil {
			return fmt.Errorf("storage: 读受影响 side_effect 动作行失败: %w", err)
		}
		for i := range affected {
			a := &affected[i]
			if !s.wouldMarkUncertain(ForceReleaseAction{
				Status: a.Status, RetrySafety: a.RetrySafety,
			}) {
				continue
			}
			// Unsafe + 在飞：转未知态等对账/人工裁决，不由租约释放默许重试。
			a.Status = sideEffectStatusUncertain
			a.Reason = appendReleaseReason(a.Reason, in.Reason)
			out.MarkedUncertain++
			if err := s.writeAction(tx, a, now); err != nil {
				return err
			}
		}
		// 资源行：只清持有状态，fence 保持（令牌代际继续单调）。
		if err := tx.Model(&sideEffectFenceRow{}).Where("resource = ?", resource).
			Updates(map[string]any{"holder": "", "lease_until": nil, "updated_at": now}).Error; err != nil {
			return fmt.Errorf("storage: 释放 side_effect 资源行失败: %w", err)
		}
		// 动作行：清执行意图/对账占用（status/result 除上面的 uncertain 转移外原样）。
		res := tx.Model(&sideEffectRow{}).
			Where(releaseAffectedWhere, resource).
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
		out.ClearedActions = res.RowsAffected
		// 持久化审计（同事务）：释放发生则审计必在。
		// #109：审计归属列从资源串前缀解析（tenant.Key 形态 "<tid>/pr:..."；
		// legacy 无前缀资源 = default——与 effect.ParseResourceTenant 同判据）。
		auditTenant := tenant.Default
		if i := strings.IndexByte(resource, '/'); i > 0 && !strings.Contains(resource[:i], ":") {
			auditTenant = resource[:i]
		}
		auditRow := SideEffectReleaseAudit{
			TenantID: auditTenant,
			Resource: resource, Holder: rr.Holder, Fence: rr.Fence,
			ClearedActions: out.ClearedActions, MarkedUncertain: out.MarkedUncertain,
			OperatorFP: in.OperatorFP, Reason: in.Reason, CreatedAt: now,
		}
		if err := tx.Create(&auditRow).Error; err != nil {
			return fmt.Errorf("storage: 落强制释放审计失败: %w", err)
		}
		out.AuditID = auditRow.ID
		return nil
	})
	return out, err
}

// appendReleaseReason 转入 uncertain 时留痕：保留原失败理由，追加强释事实。
func appendReleaseReason(prev, reason string) string {
	out := prev
	if out != "" {
		out += "; "
	}
	out += "forced release"
	if reason != "" {
		out += ": " + reason
	}
	return out
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
