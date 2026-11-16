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

// startSideEffectReconciler 注册未知态对账 ticker（副作用闸开启且回帖器已装配）。
func startSideEffectReconciler(wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, log logx.Logger) {
	if !effect.Enabled() || sys == nil || sys.Poster == nil {
		return
	}
	interval := sideEffectReconcileInterval(log)
	if interval <= 0 {
		log.Info("argus.boot.sideeffect_reconcile_disabled", "env", envSideEffectReconcile,
			"interval", interval.String())
		return
	}
	rc := effect.NewReconciler(store, effect.NewCommentResolver(sys.Poster), log)
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
