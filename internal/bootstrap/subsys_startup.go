package bootstrap

import (
	"context"
	"os"
	"time"

	oagentworker "github.com/oagentkit/queue/worker"
	oekitbootstrap "github.com/oekit/bootstrap"

	"github.com/oekit/concurrency/async"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/health"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/poller"
	"github.com/yi-nology/argus/internal/storage"
)

// startMemKernelGCTicker 评审记忆内核生命周期（v3.153.0）：原始事件按龄回收
// （缺省 30 天，与 memory CLI gc 缺省同档）。命名空间与 dream 供给面同源——
// _org + ListPatternRepos 派生的仓桶；dream 不管的库内核不攒。
// 仅内核开启时挂载；单库故障只告警不阻断（运维面）。
func startMemKernelGCTicker(wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, log logx.Logger, instanceID string) {
	if sys.MemKernel == nil {
		return
	}
	wm.AddTicker("memory-kernel-gc", 24*time.Hour, func(ctx context.Context) {
		runOnceUnderLease(ctx, store, "github.com/yi-nology/argus/ticker/kernel-gc", instanceID, dreamLeaseTTL, func(c context.Context) {
			before := time.Now().Add(-30 * 24 * time.Hour)
			nss := []string{string(memorykernel.OrgNamespaceOf(tenant.FromContext(ctx)))}
			if repos, err := store.ListPatternRepos(c); err == nil {
				for _, r := range repos {
					nss = append(nss, string(memorykernel.NamespaceOf(
						memorykernel.Ref{Platform: r.Platform, Owner: r.Owner, Repo: r.Repo})))
				}
			}
			total := 0
			for _, ns := range nss {
				n, err := sys.MemKernel.GC(c, ns, before)
				if err != nil {
					log.Warn("argus.memorykernel.gc_failed", "ns", ns, "error", err.Error())
					continue
				}
				total += n
			}
			if total > 0 {
				log.Info("argus.memorykernel.gc_done", "namespaces", len(nss), "raw_deleted", total)
			}
		})
	})
	log.Info("argus.memorykernel.gc_enabled", "interval", "24h", "raw_max_age", "720h")
}

// startHealthReportTicker 审查健康报告（v3.11.0，G5）：周期聚合学习环状态，
// IM 推送给人看。噪声处置建议（人工门控）+ guidelines 增量建议（建议文本，
// 绝不自动改仓库文件）。
func startHealthReportTicker(wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, cfg *config.Config, log logx.Logger, instanceID string) {
	if cfg.HealthReportInterval <= 0 {
		return
	}
	hr := &health.Runner{
		Store:  store,
		Notify: sys.Notify, // nil 安全：NotifyRaw 空实现仅日志
		Cfg: health.Config{
			Learning: cfg.LearningConfig(),
			TopN:     cfg.RegretInjectTopN,
		},
		Log: log,
	}
	wm.AddTicker("health-report", cfg.HealthReportInterval, func(ctx context.Context) {
		runOnceUnderLease(ctx, store, "github.com/yi-nology/argus/ticker/health-report", instanceID, dreamLeaseTTL, func(c context.Context) {
			if err := hr.RunOnce(c); err != nil {
				log.Warn("argus.health.report_failed", "error", err.Error())
			}
		})
	})
	log.Info("argus.health.enabled", "interval", cfg.HealthReportInterval.String())
}

// resolvePollTargets 任务源仓库清单（poller 与 webhook ensure 共用一次解析）。
func resolvePollTargets(cfg *config.Config, log logx.Logger) []poller.Target {
	if cfg.PollRepos == "" {
		return nil
	}
	targets, bad := poller.ParseTargets(cfg.PollRepos)
	for _, b := range bad {
		log.Warn("argus.poller.bad_target", "item", b, "format", "platform:owner/repo")
	}
	return targets
}

// ensureWebhooksAtStartup webhook 启动期幂等确保（v5.4.1，opt-in）：异步跑
// 不延迟就绪，失败只告警——webhook 是主任务源，poller 兜底仍在。secret 每次
// 启动重断言（平台侧 write-only 不可比对，轮换自愈靠这里）。
func ensureWebhooksAtStartup(store *storage.Store, sys *System, cfg *config.Config,
	log logx.Logger, targets []poller.Target) {
	if !cfg.EnsureWebhook {
		return
	}
	if cfg.PublicURL == "" {
		log.Warn("argus.webhook_ensure.disabled", "reason", "ARGUS_ENSURE_WEBHOOK=1 但 ARGUS_PUBLIC_URL 未配置")
		return
	}
	if len(targets) == 0 {
		return
	}
	// 单飞门控（v5.4.8 多副本）：leader_leases 租约互斥——多副本同启时只有
	// 一个实例刷 48+ 仓 hook API（幂等但翻倍浪费，并发 PUT 同仓 hook 有平台侧
	// 竞态面）。拿不到 = 他副本正在 ensure：本次启动跳过（hook 已被他副本校准
	// 过；secret 轮换自愈由持有者重启时覆盖）。holder 用 hostname（pod 名天然
	// 唯一），ttl 与整体上界同量级。
	holder, _ := os.Hostname()
	if ok, lerr := store.TryAcquire(context.Background(), "webhook_ensure", holder, ensureLeaseTTL); lerr != nil {
		log.Warn("argus.webhook_ensure.lease_failed", "error", lerr.Error(), "hint", "锁面故障照常跑（幂等）")
		async.GoSafe(func() {
			ensureWebhooks(context.Background(), sys.SDK, cfg, log, targets)
		})
	} else if !ok {
		log.Info("argus.webhook_ensure.skipped_lease", "holder_hint", "他副本持有 ensure 租约，本次启动跳过")
	} else {
		async.GoSafe(func() {
			ensureWebhooks(context.Background(), sys.SDK, cfg, log, targets)
			_ = store.Release(context.Background(), "webhook_ensure", holder)
		})
	}
}

// startPoller 轮询兜底任务源（v3.0.1 改造：平台入站 webhook 不可达环境）。
func startPoller(ctx context.Context, sys *System, store *storage.Store, cfg *config.Config,
	targets []poller.Target, log logx.Logger, pollElector *oagentworker.LeaderElector) {
	if cfg.PollInterval <= 0 || len(targets) == 0 {
		return
	}
	pl := &poller.Poller{SDK: sys.SDK, Store: store, Targets: targets,
		Interval: cfg.PollInterval, SkipDraft: cfg.SkipDraft, Log: log, Bus: sys.Bus,
		IsLeader: pollElector.IsLeader}
	pl.Start(ctx)
}
