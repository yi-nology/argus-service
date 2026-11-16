package bootstrap

import (
	"context"
	"time"

	oekitbootstrap "github.com/oekit/bootstrap"
	"github.com/oekit/logx"
	"github.com/yi-nology/goagent-kernel/pii"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// dreamLeaseTTL 周期控制面 ticker 单飞租约的 ttl（v5.5.0）：覆盖单次执行
// 上界即可（dream/gc/health 均分钟级），跑完即 Release。
const dreamLeaseTTL = 10 * time.Minute

// runOnceUnderLease 周期任务的多副本单飞门（v5.5.0）：每个 tick 尝试抢
// leader_leases 短租约，抢到才执行、跑完即释放（抢不到 = 他副本正在跑或
// 刚跑过，让位）。dream 的知识库导出是写面（非幂等，双跑会双导出）；
// gc/health-report 幂等但避免双副本重复全表扫描与 IM 双发通知。锁面故障
// 降级照常跑（与 webhook ensure 同款纪律：幂等/可恢复面宁重勿漏）。
func runOnceUnderLease(ctx context.Context, st *storage.Store, key, holder string,
	ttl time.Duration, fn func(context.Context)) {
	ok, err := st.TryAcquire(ctx, key, holder, ttl)
	if err != nil {
		fn(ctx)
		return
	}
	if !ok {
		return
	}
	defer func() { _ = st.Release(ctx, key, holder) }()
	fn(ctx)
}

// StartSubsystems 后台子系统组装与启动（v3.63.0 收口：main 只剩进程生命周期）。
// 此前选主/worker 池/运维 ticker/dream/health/poller 散在 main.runApp 手工组装，
// 组合根单点原则被破坏——现在全部构造收敛于此，main 只做资源注册后的 HTTP 接线。
// instanceID 为进程身份（claim 归属/心跳/选主），由 main 生成后贯穿 store 与本函数。
//
// 资源注册顺序（AddResource 逆序关闭）：
//
//	[store(main) →] poll-leader → worker → github-app-refresher → comments(main) → [http(main)]
//	停机：HTTP 先停（新任务入口关闭）→ 评论排水 → worker 两阶段排水 60s
//	（在途自然完成，超时硬取消保持 running 待下次启动重置）→ 选主让位 → 最后关库。
func StartSubsystems(app *oekitbootstrap.App, cfg *config.Config, log logx.Logger,
	store *storage.Store, sys *System, instanceID string) {
	ctx := app.Context()

	setupPIIPolicy(cfg, log)
	pollElector := startPollLeader(ctx, app, store, log, instanceID)
	startWorkerPool(ctx, app, store, sys, cfg, log)
	wm := oekitbootstrap.NewWorkerManager(ctx)
	addPruneTickers(ctx, wm, store, sys, cfg, log)
	addOpsTickers(ctx, wm, sys, store, cfg, log)
	startDreamTicker(wm, store, sys, cfg, log, instanceID)
	startMemKernelGCTicker(wm, store, sys, log, instanceID)
	startHealthReportTicker(wm, store, sys, cfg, log, instanceID)
	targets := resolvePollTargets(cfg, log)
	ensureWebhooksAtStartup(store, sys, cfg, log, targets)
	startPoller(ctx, sys, store, cfg, targets, log, pollElector)
}

// setupPIIPolicy 出站 PII 脱敏策略装配（v6.16.0，gak pii 全局原子策略）。
// 合规优先缺省开（ARGUS_PII_MASK=0 关）；脱敏动作在 argusctx 评论出站
// choke point（maskOutboundPII），此处只装配策略开关。
func setupPIIPolicy(cfg *config.Config, log logx.Logger) {
	pii.SetEnabled(cfg.PIIMask)
	if bit, err := pii.SurfaceBit(pii.SurfaceOutput); err == nil {
		pii.SetSurfaces(bit)
	}
	if err := pii.SetStrategy(pii.StrategyMask); err != nil {
		log.Warn("argus.boot.pii_strategy", "error", err.Error())
	}
	if cfg.PIIMask {
		log.Info("argus.boot.pii_mask_enabled", "surface", string(pii.SurfaceOutput))
	}
}
