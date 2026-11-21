// side_effect_reconcile.go —— 未知态副作用自动对账的启动接线（#43）。
//
// 与其它周期任务同纪律：不裸起 goroutine，生命周期交给 WorkerManager；每个
// 实例都跑（裁决先到者为准，重复查评论只浪费平台读取，不会重复施加）。
package bootstrap

import (
	"context"
	"os"
	"strings"
	"time"

	oekitbootstrap "github.com/oekit/bootstrap"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
)

// envSideEffectReconcile 对账周期（time.ParseDuration；缺省 1m；0/负值关闭）。
const envSideEffectReconcile = "ARGUS_SIDEEFFECT_RECONCILE"

// sideEffectReconcileInterval 解析对账周期：缺失 → 缺省 1m；非法值 → 告警并
// 回落 1m；显式 0/负值原样返回（供调用方关闭）。
func sideEffectReconcileInterval(log logx.Logger) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envSideEffectReconcile))
	if raw == "" {
		return effect.DefaultReconcileInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if log != nil {
			log.Warn("argus.boot.sideeffect_reconcile_interval_invalid",
				"value", raw, "default", effect.DefaultReconcileInterval.String())
		}
		return effect.DefaultReconcileInterval
	}
	return d
}

// startSideEffectReconciler 注册未知态对账 ticker（副作用闸开启且至少一族
// resolver 可装配：评论=Poster、修复 PR=SDK、push=SDK+git 探针）。
func startSideEffectReconciler(wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, cfg *config.Config, log logx.Logger) {
	if !effect.Enabled() || sys == nil {
		return
	}
	interval := sideEffectReconcileInterval(log)
	if interval <= 0 {
		log.Info("argus.boot.sideeffect_reconcile_disabled", "env", envSideEffectReconcile,
			"interval", interval.String())
		return
	}
	// 路由分发：评论 / 修复 PR / push 三族 Key 各有判据（#64 补齐后两族），
	// 未知 Key 类型照旧 skipped 留痕。
	router := effect.NewRouterResolver(buildEffectResolvers(sys, cfg)...)
	rc := effect.NewReconciler(store, router, log)
	if err := wm.AddTicker("sideeffect-reconcile", interval, func(ctx context.Context) {
		if _, _, err := rc.ReconcileOnce(ctx); err != nil {
			log.Warn("argus.boot.sideeffect_reconcile_failed", "error", err.Error())
		}
	}); err != nil {
		log.Warn("argus.boot.sideeffect_reconcile_register_failed", "error", err.Error())
		return
	}
	log.Info("argus.boot.sideeffect_reconcile_enabled", "interval", interval.String(),
		"grace", effect.DefaultReconcileGrace.String(), "batch", effect.DefaultReconcileBatch)
}

// buildEffectResolvers 按**已装配的宿主能力**逐族注册：Poster 缺席则评论族
// 跳过（不把「没装查询能力」误判成「评论不存在」——resolver 内 nil 判据），
// SDK 缺席则修复 PR/push 的平台判据跳过。
func buildEffectResolvers(sys *System, cfg *config.Config) []effect.UncertainResolver {
	var rs []effect.UncertainResolver
	if sys.Poster != nil {
		rs = append(rs, effect.NewCommentResolver(sys.Poster))
	}
	if sys.SDK != nil {
		rs = append(rs, effect.NewCRResolver(sdkCRFinder{sdk: sys.SDK}))
		rs = append(rs, effect.NewPushResolver(effectPushProbe{cfg: cfg, sdk: sys.SDK}))
		rs = append(rs, effect.NewMergeResolver(sys.SDK))
	}
	return rs
}
