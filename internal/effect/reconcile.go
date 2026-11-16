// reconcile.go —— 未知态副作用的周期自动对账。
//
// side_effects.status=uncertain 表示「动作可能已生效、也可能没有」，此前只能
// 永久阻塞等人工。本文件把它变成可收敛的周期任务：按龄取未知态 Key，交给
// resolver 查下游事实，再用 ResolveUncertainKey 落裁决。
//
// 纪律：
//   - 只碰 updated_at 早于 grace 的行（给施加方留出「失败→标未知→释放」窗口，
//     避免与在途动作抢判）；
//   - resolver 说不支持（ok=false）→ skipped，不改状态（未知 Key 类型留痕等
//     后续扩展，绝不猜）；
//   - 单行失败不阻断其余行，错误汇总返回；
//   - 不在此处起 goroutine：周期由 bootstrap 的 WorkerManager 管理。
package effect

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/storage"
)

const (
	// DefaultReconcileInterval 缺省对账周期（ARGUS_SIDEEFFECT_RECONCILE 覆盖）。
	DefaultReconcileInterval = time.Minute
	// DefaultReconcileGrace 未知态行的最短静置时间：给施加方留出标记/释放窗口。
	DefaultReconcileGrace = 5 * time.Minute
	// DefaultReconcileBatch 单轮最多处理的 Key 数。
	DefaultReconcileBatch = 50
)

// EventReconcile 对账日志事件名（key/resource/result/reason 字段）。
const EventReconcile = "argus.sideeffect.reconcile"

// UncertainResolver 把一条未知态 Key 判定为裁决（或声明类型不受支持）。
type UncertainResolver interface {
	Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error)
}

// Reconciler 未知态 Key 的周期对账器。
type Reconciler struct {
	store    *storage.Store
	resolver UncertainResolver
	interval time.Duration
	grace    time.Duration
	batch    int
	log      logx.Logger
}

// NewReconciler 组装对账器（周期/静置/批量取缺省值）。
func NewReconciler(store *storage.Store, resolver UncertainResolver, log logx.Logger) *Reconciler {
	if log == nil {
		log = logx.Nop()
	}
	return &Reconciler{
		store:    store,
		resolver: resolver,
		interval: DefaultReconcileInterval,
		grace:    DefaultReconcileGrace,
		batch:    DefaultReconcileBatch,
		log:      log,
	}
}

// ReconcileOnce 执行一轮对账，返回已裁决（applied+retryable）与跳过计数。
// 单行错误不阻断其余行，最终用 errors.Join 汇总。
func (r *Reconciler) ReconcileOnce(ctx context.Context) (resolved, skipped int, err error) {
	if r == nil || r.store == nil || r.resolver == nil {
		return 0, 0, errors.New("effect: 对账器未装配 store/resolver")
	}
	grace := r.grace
	if grace < 0 {
		grace = 0
	}
	rows, err := r.store.SideEffects().ListUncertain(ctx, time.Now().UTC().Add(-grace), r.batch)
	if err != nil {
		return 0, 0, err
	}
	var errs []error
	for _, u := range rows {
		if cerr := ctx.Err(); cerr != nil {
			errs = append(errs, cerr)
			break
		}
		res, ok, rerr := r.resolver.Resolve(ctx, u)
		if rerr != nil {
			obs.IncrSideeffectReconcile("error")
			r.log.Warn(EventReconcile, "key", u.Key, "resource", u.Resource,
				"result", "error", "reason", rerr.Error())
			errs = append(errs, fmt.Errorf("effect: 对账 Key %q 失败: %w", u.Key, rerr))
			continue
		}
		if !ok {
			skipped++
			obs.IncrSideeffectReconcile("skipped")
			r.log.Info(EventReconcile, "key", u.Key, "resource", u.Resource,
				"result", "skipped", "reason", "unknown key type")
			continue
		}
		outcome, _, aerr := r.store.SideEffects().ResolveUncertainKey(ctx, u.Key, res)
		if aerr != nil {
			obs.IncrSideeffectReconcile("error")
			r.log.Warn(EventReconcile, "key", u.Key, "resource", u.Resource,
				"result", "error", "reason", aerr.Error())
			errs = append(errs, fmt.Errorf("effect: 裁决 Key %q 失败: %w", u.Key, aerr))
			continue
		}
		result, reason := "retryable", res.Reason
		if res.Applied {
			result = "applied"
		}
		if reason == "" {
			reason = outcome.String()
		}
		resolved++
		obs.IncrSideeffectReconcile(result)
		r.log.Info(EventReconcile, "key", u.Key, "resource", u.Resource,
			"result", result, "reason", reason)
	}
	return resolved, skipped, errors.Join(errs...)
}

// Run 周期执行对账，直到 ctx 取消。间隔为完成驱动（上一轮返回后再等 interval），
// 与 WorkerManager 的 ticker 语义一致；生产接线走 WorkerManager，本方法供
// 独立进程/测试使用。
func (r *Reconciler) Run(ctx context.Context) {
	if r == nil || r.interval <= 0 {
		return
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, _, err := r.ReconcileOnce(ctx); err != nil && ctx.Err() == nil {
				r.log.Warn("argus.sideeffect.reconcile_failed", "error", err.Error())
			}
		}
	}
}
