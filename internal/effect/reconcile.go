// reconcile.go —— 未知态副作用的周期自动对账。
//
// side_effects.status=uncertain 表示「动作可能已生效、也可能没有」，此前只能
// 永久阻塞等人工。本文件把它变成可收敛的周期任务：按龄取未知态 Key，交给
// resolver 查下游事实，再落裁决。
//
// 底座收编（oekit 批2，runtime#1 发版后改指）：枚举/静置窗/三态分发/落库
// 结算/ErrNotUncertain 并发分类/逐项观测 全在 goagent-runtime/adjudicate
// （Loop 单轮无 goroutine）；本文件只剩宿主接线——storage 投影 ↔ Item 适配、
// UncertainResolver ↔ adjudicate.Resolver 适配、obs/logx 观测面（Observer
// 钩子逐项还原既有指标与日志字段），以及 WorkerManager 周期驱动。
//
// 纪律（与改指前逐字一致）：
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

	"github.com/oagentkit/goagent-kernel/sideeffect"
	"github.com/oagentkit/goagent-runtime/adjudicate"

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

// RouterResolver 按声明顺序试一组 resolver，第一个「认识这个 Key」的裁决生效
// （#64：评论/push/修复 PR 三族 Key 各有判据，分发在这里收口，Reconciler 无感）。
// 语义：
//   - 某 resolver 返回 error（认识但不裁决不了）→ 立即透传（判据不足，本轮不裁决）；
//   - 某 resolver 返回 ok=true → 透传其裁决；
//   - 全部 ok=false（无人认识）→ (空, false, nil)——未知 Key 类型留痕等后续扩展。
type RouterResolver struct {
	resolvers []UncertainResolver
}

// NewRouterResolver 组装路由裁决器（nil 项忽略）。
func NewRouterResolver(rs ...UncertainResolver) *RouterResolver {
	out := make([]UncertainResolver, 0, len(rs))
	for _, r := range rs {
		if r != nil {
			out = append(out, r)
		}
	}
	return &RouterResolver{resolvers: out}
}

// Resolve 按声明顺序分发（见类型注释）。
func (r *RouterResolver) Resolve(ctx context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	if r == nil {
		return sideeffect.Resolution{}, false, nil
	}
	for _, res := range r.resolvers {
		out, ok, err := res.Resolve(ctx, u)
		if err != nil || ok {
			return out, ok, err
		}
	}
	return sideeffect.Resolution{}, false, nil
}

// Reconciler 未知态 Key 的周期对账器（底座=adjudicate.Loop；grace/batch
// 字段保持构造后可改——测试与调参面直接赋值，每轮装配 Loop 时读取）。
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

// storeSource storage 投影 → adjudicate.Source 适配：UncertainSideEffect
// 扁平行还原完整 Action（payload 字节随列取回，身份面全量——批2B 补列前置）。
type storeSource struct {
	store *storage.Store
}

// ListUncertain 按龄枚举未知态 Key（grace 截止时间由 Loop 计算传入）。
// olderThan 归一 UTC：storage 的行时刻是 UTC 时钟（SideEffectStore.now），
// SQLite 方言把时间存文本、比较是字典序——本地时区截止串与 UTC 存储串
// 混比会让新鲜行漏过滤（grace 失效实测抓获）；PG/MySQL 为瞬时比较无此
// 面，归一无副作用。
func (s *storeSource) ListUncertain(ctx context.Context, olderThan time.Time, limit int) ([]adjudicate.Item, error) {
	rows, err := s.store.SideEffectAdmin().ListUncertain(ctx, olderThan.UTC(), limit)
	if err != nil {
		return nil, err
	}
	items := make([]adjudicate.Item, 0, len(rows))
	for _, u := range rows {
		items = append(items, adjudicate.Item{
			Action: sideeffect.Action{
				Key:         sideeffect.Key(u.Key),
				Resource:    u.Resource,
				Payload:     u.Payload,
				RetrySafety: sideeffect.RetrySafety(u.RetrySafety),
			},
			Reason:   u.Reason,
			MarkedAt: u.UpdatedAt,
		})
	}
	return items, nil
}

// resolverAdapter adjudicate.Resolver → UncertainResolver 适配：Item 还原
// 扁平投影喂给域裁决器（comment/cr/merge/push 四族零改动）。
type resolverAdapter struct {
	inner UncertainResolver
}

// Resolve 还原投影后转交（见类型注释）。
func (a *resolverAdapter) Resolve(ctx context.Context, item adjudicate.Item) (sideeffect.Resolution, bool, error) {
	if a.inner == nil {
		return sideeffect.Resolution{}, false, nil
	}
	return a.inner.Resolve(ctx, storage.UncertainSideEffect{
		Key:         string(item.Action.Key),
		Resource:    item.Action.Resource,
		PayloadHash: sideeffect.HashPayload(item.Action.Payload),
		Payload:     item.Action.Payload,
		RetrySafety: int16(item.Action.RetrySafety),
		Reason:      item.Reason,
		UpdatedAt:   item.MarkedAt,
	})
}

// observerFor 逐项观测钩子：把 adjudicate 的结算事件还原为既有 obs 指标与
// 结构化日志（字段/取值与改指前逐字一致——告警面零漂移）。
func observerFor(log logx.Logger) adjudicate.Observer {
	return func(ctx context.Context, o adjudicate.Observation) {
		key := string(o.Item.Action.Key)
		resource := o.Item.Action.Resource
		switch o.Event {
		case adjudicate.EventSettled:
			result := "retryable"
			if o.Resolution.Applied {
				result = "applied"
			}
			reason := o.Resolution.Reason
			if reason == "" {
				reason = o.Outcome.String()
			}
			obs.IncrSideeffectReconcile(ctx, result)
			log.Info(EventReconcile, "key", key, "resource", resource,
				"result", result, "reason", reason)
		case adjudicate.EventSkipped:
			// Err 非 nil = 并发已裁决（ErrNotUncertain，Loop 已分类进 skipped）。
			reason := "unknown key type"
			if o.Err != nil {
				reason = "resolved by another reconciler"
			}
			obs.IncrSideeffectReconcile(ctx, "skipped")
			log.Info(EventReconcile, "key", key, "resource", resource,
				"result", "skipped", "reason", reason)
		case adjudicate.EventDeferred, adjudicate.EventFailed:
			reason := ""
			if o.Err != nil {
				reason = o.Err.Error()
			}
			obs.IncrSideeffectReconcile(ctx, "error")
			log.Warn(EventReconcile, "key", key, "resource", resource,
				"result", "error", "reason", reason)
		}
	}
}

// ReconcileOnce 执行一轮对账，返回已裁决（applied+retryable）与跳过计数。
// 单行错误不阻断其余行，最终带首个错误返回（Errors.Is 可穿透，见 adjudicate）。
func (r *Reconciler) ReconcileOnce(ctx context.Context) (resolved, skipped int, err error) {
	if r == nil || r.store == nil || r.resolver == nil {
		return 0, 0, errors.New("effect: 对账器未装配 store/resolver")
	}
	grace := r.grace
	if grace < 0 {
		grace = 0
	}
	// 每轮装配 Loop（Loop 无跨轮状态）：grace/batch 字段的构造后赋值面
	// （测试 r.grace=0 / 调参）在此生效。
	loop, lerr := adjudicate.NewLoop(
		&storeSource{store: r.store},
		&resolverAdapter{inner: r.resolver},
		sideeffect.NewRunner(r.store.SideEffects(), DefaultTTL),
		adjudicate.WithGrace(grace),
		adjudicate.WithBatch(r.batch),
		adjudicate.WithObserver(observerFor(r.log)),
	)
	if lerr != nil {
		return 0, 0, fmt.Errorf("effect: 对账循环装配失败: %w", lerr)
	}
	st, lerr := loop.ReconcileOnce(ctx)
	return st.Settled, st.Skipped, lerr
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
