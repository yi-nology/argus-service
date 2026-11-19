package bootstrap

import (
	"context"
	"time"

	oagentworker "github.com/oagentkit/queue/worker"
	oekitbootstrap "github.com/oekit/bootstrap"
	argusworker "github.com/yi-nology/argus/internal/worker"

	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dream"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/runner"
	"github.com/yi-nology/argus/internal/storage"
)

// startPollLeader 多副本控制面选主（oagentkit/queue/worker.LeaderElector）。
// 任务面（worker pool）靠 ClaimNextPending 原子抢占天然分片，无需选主；
// 轮询器是"只能跑一份"的控制面组件——租约收敛，失联 ≤ttl 自动换主。
// 返回 elector 供末段轮询兜底的 IsLeader 判定。
func startPollLeader(ctx context.Context, app *oekitbootstrap.App, store *storage.Store,
	log logx.Logger, instanceID string) *oagentworker.LeaderElector {
	pollElector := oagentworker.NewLeaderElector(store, "github.com/yi-nology/argus/poller", instanceID,
		30*time.Second, 10*time.Second, oagentworker.WithLeaderLogger(log))
	pollElector.Start(ctx)
	app.AddResource("poll-leader", func(context.Context) error {
		pollElector.Stop() // 优雅停机主动让位，缩短换主窗口
		return nil
	})
	return pollElector
}

// startWorkerPool worker pool 装配（§10.4 DB 即队列；两阶段优雅停机）。
// GitHub App refresher 停机收编已随 v5.3.2 TokenSource 迁移移除：installation
// token 改为每请求取值 + 临期懒续期，无后台 goroutine，停机序列无 SDKCtx 侧动作。
func startWorkerPool(ctx context.Context, app *oekitbootstrap.App, store *storage.Store,
	sys *System, cfg *config.Config, log logx.Logger) {
	wp := &oagentworker.Pool{Queue: &argusworker.StoreQueue{Store: store},
		N: cfg.MaxConcurrentTasks, Log: log, Run: sys.Runner.Execute}
	wp.Start(ctx)
	// 排水宽限可配（v3.8.0 ARGUS_WORKER_GRACE，默认 60s）：grace < 典型任务时长时
	// 每次发布必打断重跑；与 ShutdownTimeout 的余量关系见 runbook。
	app.AddResource("worker", func(context.Context) error {
		wp.Stop(cfg.WorkerGrace)
		return nil
	})
}

// addTicker fail-loud 注册 ticker：重名等注册失败 Warn 留痕（静默缺席 =
// 该清理/观测环永不运转，事后无法从指标察觉）。不中断兄弟 ticker 注册。
func addTicker(wm *oekitbootstrap.WorkerManager, log logx.Logger, name string,
	interval time.Duration, fn func(context.Context)) {
	if err := wm.AddTicker(name, interval, fn); err != nil {
		log.Warn("argus.boot.ticker_register_failed", "name", name, "error", err.Error())
	}
}

// taskDeleteGrace 软删除恢复宽限（#109a）：retention 到期置 deleted_at 后，
// 再过 7 天才物理清除——「删除后 N 天可恢复」的合同口径；窗口内 RestoreTask
// 可复活任务（event_id 已在软删时释放，恢复后语义自洽）。
const taskDeleteGrace = 7 * 24 * time.Hour

// addPruneTickers 保留清理面 ticker（WorkerManager 管理生命周期）：任务保留、
// 预算预留、会话/指纹等按龄回收。ctx 取消时所有 ticker 自动退出
// （cancel 先于 close），无需显式 Close。
func addPruneTickers(ctx context.Context, wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, cfg *config.Config, log logx.Logger) {
	addTicker(wm, log, "workcopy-scavenger", time.Hour, func(context.Context) { sys.WC.Sweep(time.Hour) })
	// spill 存档过期清理（v5.1.0，runner.PruneSpillRoot）：7 天前任务目录整删，
	// 与工作副本清扫同周期——此前挂每任务 wireWorkflow，任务风暴下是重复全扫。
	addTicker(wm, log, "spill-prune", time.Hour, func(context.Context) { runner.PruneSpillRoot(cfg.WorkRoot) })
	if cfg.TaskRetentionDays > 0 {
		retention := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		pruneOnce := func(ctx context.Context) {
			if n, err := store.PruneTasks(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("argus.boot.prune_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned", "removed", n, "retention_days", cfg.TaskRetentionDays)
			}
			// 逐调用账本随任务保留期同 cutoff 清理（#32）：usage_event/已收口预留
			// 与被清任务同龄出局（事件龄 ≥ 任务龄，序上不会先于任务被清）。
			// #109a 软删除第二阶：软删 grace（7 天）窗口到期后物理清除——
			// 「删除后 7 天可恢复」合同口径（第一阶 deleted_at 由上方
			// PruneTasks 置位，本阶只清 graceCutoff 之前的软删行）。
			if n, err := store.PruneTasksHard(ctx, time.Now().Add(-(retention + taskDeleteGrace))); err != nil {
				log.Warn("argus.boot.prune_tasks_hard_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned_tasks_hard", "removed", n)
			}
			if n, err := store.PruneUsageEvents(ctx, time.Now().Add(-retention)); err != nil {
				log.Warn("argus.boot.prune_usage_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.pruned_usage", "removed", n)
			}
		}
		pruneOnce(ctx)
		addTicker(wm, log, "task-prune", 24*time.Hour, pruneOnce)
	} else {
		log.Warn("argus.boot.retention_disabled", "hint",
			"ARGUS_TASK_RETENTION_DAYS=0（永久保留）——tasks 报告/调试产物每任务可达 MB 级单调增长；默认 30 天，显式关闭请确认磁盘监控到位（docs/ops/runbook.md 场景 3）")
	}
	// 过期预算预留清扫（#32 验收④租约/超时兜底）：持有进程死亡 → 心跳停 →
	// 预留租约（5min）过期 → 置 released 归还额度。查询侧本就按 expires_at
	// 过滤（自愈），本环只做状态收敛与日志留痕；10min 周期 > TTL 一倍余量。
	addTicker(wm, log, "budget-reserve-sweep", 10*time.Minute, func(ctx context.Context) {
		if n, err := store.ReleaseExpiredReservations(ctx, time.Now()); err != nil {
			log.Warn("argus.boot.budget_reserve_sweep_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.budget_reserve_released", "expired", n)
		}
	})
	addAuxPruneTicker(ctx, wm, store, cfg, log)
}

// addAuxPruneTicker 会话与指纹的保留清理（深挖修复：qa_sessions/finding_prints
// 此前永不清理，history_json/comment 单调膨胀拖慢全表扫描与备份）。
// qa_sessions：上下文 TTL 24h，7 天前会话无回放价值；finding_prints：长期
// 未复现（last_seen_at）的指纹遗忘——最坏效果是同型意见重新从头计数，不会
// 复活误报（保留期给足 180 天起）。
func addAuxPruneTicker(ctx context.Context, wm *oekitbootstrap.WorkerManager, store *storage.Store,
	cfg *config.Config, log logx.Logger) {
	qaCutoff := 7 * 24 * time.Hour
	printCutoff := 180 * 24 * time.Hour
	if cfg.TaskRetentionDays > 0 {
		r := time.Duration(cfg.TaskRetentionDays) * 24 * time.Hour
		if r > qaCutoff {
			qaCutoff = r
		}
		if r*3 > printCutoff {
			printCutoff = r * 3
		}
	}
	pruneAuxOnce := func(ctx context.Context) {
		if n, err := store.PruneQASessions(ctx, time.Now().Add(-qaCutoff)); err != nil {
			log.Warn("argus.boot.qa_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.qa_pruned", "removed", n)
		}
		if n, err := store.PruneStalePrints(ctx, time.Now().Add(-printCutoff)); err != nil {
			log.Warn("argus.boot.print_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.print_pruned", "removed", n)
		}
		// 评论投递幂等记录（v3.8.0 随幂等下沉 DB 而来）：7 天前的重试已无幂等意义
		if n, err := store.PruneCommentDeliveries(ctx, time.Now().Add(-7*24*time.Hour)); err != nil {
			log.Warn("argus.boot.delivery_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.delivery_pruned", "removed", n)
		}
		// 模式信号（v3.9.2）：与指纹同保留期（长期未复现即遗忘，重新从头计数）
		if n, err := store.PruneStalePatterns(ctx, time.Now().Add(-printCutoff)); err != nil {
			log.Warn("argus.boot.pattern_prune_failed", "error", err.Error())
		} else if n > 0 {
			log.Info("argus.boot.pattern_pruned", "removed", n)
		}
	}
	pruneAuxOnce(ctx)
	addTicker(wm, log, "session-print-prune", 24*time.Hour, pruneAuxOnce)
}

// addOpsTickers 运维观测面 ticker：事件 Outbox fanout（v6.9.0 P1-05，#10）
// + 副作用对账指标（P2-03，#14）+ failed 重试 + 队列深度 + golden 评测指标。
func addOpsTickers(ctx context.Context, wm *oekitbootstrap.WorkerManager, sys *System,
	store *storage.Store, cfg *config.Config, log logx.Logger) {
	// 每实例回灌环：outbox 新行 → 本地 Bus → SSE（他实例事件经此送达本实例客户端）。
	sys.Service.StartOutboxFanout(ctx)
	reconcileOnce := func(ctx context.Context) {
		postedButFailed, doneWithoutReply, err := store.OutOfSyncDeliveries(ctx, time.Now().Add(-24*time.Hour))
		if err != nil {
			log.Warn("argus.boot.reconcile_failed", "error", err.Error())
			return
		}
		if postedButFailed > 0 || doneWithoutReply > 0 {
			obs.IncrSideEffectMismatch(ctx, postedButFailed+doneWithoutReply)
			log.Warn("argus.boot.reconcile_mismatch", "posted_but_failed", postedButFailed,
				"done_without_reply", doneWithoutReply,
				"hint", "任务终态与回帖副作用不一致（P2-03 对账面）——按 task_id 排查 summary_comment_id 与任务终态时序")
		}
	}
	reconcileOnce(ctx)
	addTicker(wm, log, "side-effect-reconcile", time.Hour, reconcileOnce)
	// failed 任务到期重试（v3.8.0 ARGUS_TASK_MAX_RETRIES，默认 2；0=禁用）：
	// 指数退避到期后重置 pending；多副本同时执行幂等（RowsAffected 至多一方非零）。
	if cfg.TaskMaxRetries > 0 {
		addTicker(wm, log, "failed-retry", time.Minute, func(ctx context.Context) {
			if n, err := store.ResetFailedForRetry(ctx, cfg.TaskMaxRetries, time.Now()); err != nil {
				log.Warn("argus.boot.failed_retry_failed", "error", err.Error())
			} else if n > 0 {
				log.Info("argus.boot.failed_retried", "reset", n, "max_retries", cfg.TaskMaxRetries)
			}
		})
	}
	addTicker(wm, log, "queue-depth", 30*time.Second, func(ctx context.Context) {
		depths, err := store.TaskCountsByTenant(ctx)
		if err != nil {
			log.Warn("argus.boot.queue_depth_failed", "error", err.Error())
			return
		}
		obs.SetTenantQueueDepths(depths)
	})
	// golden 评测结果指标（v3.14.0，G1/G6）：eval-real 离线落盘 latest.json，
	// 这里周期加载暴露 argus_eval_*——审查质量趋势与运行时指标同面抓取。
	// 文件不存在（未跑过评测）与增量快照（评测进行中）静默跳过。
	refreshEvalMetrics := func() {
		if err := obs.RefreshEvalResults(cfg.EvalResultsPath); err != nil &&
			!obs.IsNotExistRefreshErr(err) && !obs.IsIncompleteRefreshErr(err) {
			log.Warn("argus.boot.eval_metrics_refresh_failed", "error", err.Error())
		}
	}
	refreshEvalMetrics()
	addTicker(wm, log, "eval-results", 5*time.Minute, func(ctx context.Context) { refreshEvalMetrics() })
}

// startDreamTicker dream 长期记忆整合（v3.5.1）：去重 pattern_signals +
// 导出低置信模式到知识库。仅在学习环开启且显式配置周期时启动（默认 0=关闭）。
func startDreamTicker(wm *oekitbootstrap.WorkerManager, store *storage.Store,
	sys *System, cfg *config.Config, log logx.Logger, instanceID string) {
	if !cfg.RegretLearning || cfg.DreamInterval <= 0 {
		return
	}
	dm := &dream.Dream{
		Store:        store,
		Knowledge:    sys.Knowledge,
		KnowledgeDir: cfg.KnowledgeDir,
		Kernel:       sys.MemKernel,        // v5.0.0 记忆唯一路径（nil = 记忆面哑掉）
		Learning:     cfg.LearningConfig(), // 前置门已保证 RegretLearning=true
		ExportTopN:   cfg.RegretInjectTopN,
		Log:          log,
	}
	addTicker(wm, log, "dream", cfg.DreamInterval, func(ctx context.Context) {
		runOnceUnderLease(ctx, store, "github.com/yi-nology/argus/ticker/dream", instanceID, dreamLeaseTTL, func(c context.Context) {
			if _, _, err := dm.RunOnce(c); err != nil {
				log.Warn("argus.dream.run_failed", "error", err.Error())
			}
		})
	})
	log.Info("argus.dream.enabled", "interval", cfg.DreamInterval.String())
}
