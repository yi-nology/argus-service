// Package obs 运行时观测（design §10.1）：argus_* 指标定义并挂到 oekit/metrics.Registry
// （对齐工作区 kube-prometheus-stack 接入），在 router.go 暴露 /metrics。
//
// 租户维度（#110）：任务域指标一律带 tenant label（ctx 经 tenant.FromContext
// 提取，nil ctx 回落 default），per-tenant SLO/告警/计费视图由此可得。刻意
// 不带租户的剩余面：sseConnections（服务端全局连接态）、migrationLeaseLost
// （基础设施面）、evalmetrics（离线基准质量，非租户域）。
package obs

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/oekit/metrics"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

var (
	tasksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_tasks_total", Help: "任务终态计数（吞吐与失败率）",
	}, []string{"platform", "kind", "status", "tenant"})

	stageDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_stage_duration_seconds",
		Help:    "R1-R5 阶段耗时分布",
		Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600},
	}, []string{"stage", "tenant"})

	pluginDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_plugin_duration_seconds",
		Help:    "插件耗时（熔断依据的可视化）",
		Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"plugin", "tenant"})

	pluginFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_failures_total", Help: "插件失败计数（含 poster）",
	}, []string{"plugin", "tenant"})

	llmTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_tokens_total", Help: "LLM token 消耗（成本核算；聚合计数器，不做预算判定）",
	}, []string{"stage", "model", "tenant"})

	// llmCachedTokens prompt 缓存命中 token（model）。前缀缓存命中率 = cached/prompt，
	// 命中 token 计价折扣（省 token 盲区修复：此前 CachedTokens 被采集但零消费，
	// 缓存命中率不可见、成本按全价入账高估）。
	llmCachedTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_cached_tokens_total", Help: "LLM prompt 缓存命中 token（前缀缓存命中率=cached/prompt）",
	}, []string{"model", "tenant"})
	llmPromptTokens = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_prompt_tokens_total", Help: "LLM prompt token（缓存命中率分母）",
	}, []string{"model", "tenant"})

	findingsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_findings_total", Help: "审查产出分布",
	}, []string{"severity", "plugin", "tenant"})

	findingsByRepo = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_findings_by_repo_total", Help: "按仓 findings 趋势（密度/异常仓识别）",
	}, []string{"platform", "repo", "severity", "tenant"})

	taskDurationByRepo = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "argus_task_duration_seconds_by_repo",
		Help:    "按仓任务耗时分布（审查延迟趋势）",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 900, 1800},
	}, []string{"platform", "repo", "kind", "tenant"})

	// tasksPending/tasksRunning（#110 租户化）：由全局 gauge 改 GaugeVec{tenant}——
	// 队列深度是最常被按租户 SLO 盯的指标，全局形态让多租户积压不可见。
	tasksPending = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "argus_tasks_pending", Help: "当前 pending 任务数（按租户；队列积压观测）",
	}, []string{"tenant"})
	tasksRunning = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "argus_tasks_running", Help: "当前 running 任务数（按租户）",
	}, []string{"tenant"})
	notifyFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_notify_failures_total", Help: "IM 通知推送失败计数（octopus 可达性观测）",
	}, []string{"channel", "tenant"})

	reflectDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_reflect_dropped_total", Help: "R3.5 证据门 strict 丢弃计数",
	}, []string{"reason", "tenant"})

	reflectDegraded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_reflect_degraded_total", Help: "R3.5 证据门 soft 降级计数",
	}, []string{"reason", "tenant"})

	ctxFit = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_ctxfit_total", Help: "上下文窗口拟合计数（stage×action；passthrough 占绝对多数为常态）",
	}, []string{"stage", "action", "tenant"})

	llmContinued = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_continued_total", Help: "输出截断续写轮次（stage；length 截断的续写救济）",
	}, []string{"stage", "tenant"})

	toolAnomaly = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_tool_anomaly_total", Help: "ReAct 循环异常守卫拦截计数（同名同参连续重复调用）",
	}, []string{"tool", "tenant"})

	ctxBreakdown = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_ctx_breakdown_runes_total", Help: "上下文构成分解（stage×role 的 rune 累计量；FitModel 发送路径顺手分桶）",
	}, []string{"stage", "role", "tenant"})

	r2SoftFailure = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_r2_soft_failures_total", Help: "R2 变更摘要软失败计数（输出空/过短，重试后仍无效走降级）",
	}, []string{"tenant"})

	// layerExec 分层活检：层被真正走到的次数。increase()==0 且 tasks_total{done}>0
	// → 该层静默失效（配置关掉的层不注册/不打点，不误报）。
	layerExec = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_layer_exec_total", Help: "分层执行探针（活检：名义存在但零执行）",
	}, []string{"layer", "tenant"})

	// verdicts R5 真值表命中分布（深挖补充：此前裁决分布只能查库）。
	verdicts = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_verdicts_total", Help: "裁决分布（rule=R5.0..R5.5）",
	}, []string{"platform", "kind", "verdict", "rule", "tenant"})

	// fenceNeutralized 围栏中和计数：注入特征文本出现信号（防线 #1 观测面，
	// 此前为纯字符串函数零留痕）。按租户可见 = 哪个租户正被注入试探。
	fenceNeutralized = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_fence_neutralized_total", Help: "DataFence 中和的围栏标记序列数（注入攻击信号，按租户）",
	}, []string{"tenant"})

	// pluginSkipped 插件跳过原因分布（circuit/budget/canceled）。
	pluginSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_skipped_total", Help: "插件未参与原因分布",
	}, []string{"reason", "tenant"})

	// commentRoutes 评论意图路由分布。
	commentRoutes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_route_total", Help: "@argus 评论意图路由分布",
	}, []string{"route", "via", "tenant"})

	// taskDeduped 任务创建去重分布。
	taskDeduped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_task_create_deduped_total", Help: "webhook 任务去重分布（幂等观测）",
	}, []string{"reason", "tenant"})

	// sseConnections 当前 SSE 订阅连接数（服务端全局态，不带租户维度）。
	sseConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "argus_sse_connections", Help: "当前 /events SSE 订阅连接数",
	})

	// webhookSkipped 事件被白名单/规则过滤分布：此前 skipped() 零日志零指标，
	// "整类事件被丢"（如 v3.7.x 白名单词表与归一化脱节导致 cr.updated 断流）
	// 在生产完全不可观测。
	webhookSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_skipped_total", Help: "webhook 事件被规则跳过分布（event_type/bot_self/missing_cr/draft）",
	}, []string{"reason", "tenant"})

	// webhookRejected webhook 入口拒绝分布（安全信号：签名拒绝/无 secret 拒绝）。
	webhookRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_rejected_total", Help: "webhook 入口拒绝分布（no_secret/signature/parse/body_too_large）",
	}, []string{"reason", "tenant"})

	// webhookEnsure 启动期幂等确保动作分布（v5.4.2 补观测）：created/updated/
	// unchanged/secret_patched/skipped/failed——ensure 是 opt-in 新通道，没指标
	// 就只能翻日志核实它是否真的在工作（与 v3.7.x skipped 盲区同型教训）。
	webhookEnsure = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_webhook_ensure_total", Help: "启动期 webhook 幂等确保动作分布（created/updated/unchanged/secret_patched/skipped/conflict/failed）",
	}, []string{"action", "tenant"})

	// llmFailovers LLM 主备切换计数：此前只有 Warn 日志，切换频率无法聚合告警。
	llmFailovers = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_llm_failovers_total", Help: "LLM 主→备切换次数（fallback/failover）",
	}, []string{"stage", "tenant"})

	// qaFailures 评论链路 LLM 失败计数。
	qaFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_qa_failures_total", Help: "评论链路处理失败分布（llm/reject/…）",
	}, []string{"reason", "tenant"})

	// testGenVerify testgen 沙箱验证结果计数（v3.22.0）。
	testGenVerify = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_testgen_verify_total", Help: "testgen 沙箱验证结果分布（passed/failed）",
	}, []string{"result", "tenant"})

	// ---- 阶段 3（v7.1 生产运营）首切：修复闭环运营指标 ----
	// 修复验证环结果（分子分母的分母侧）：verifyFixes 对带 suggested_fix 的
	// 意见逐条跑沙箱验证，passed/failed——「自动修复有效率」的输入面。
	fixVerify = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_fix_verify_total", Help: "修复沙箱验证结果分布（passed/failed；阶段 3 有效率分母）",
	}, []string{"result", "tenant"})
	// 修复推送环结果（mode=branch|direct）：result 词表
	// pushed=分支已推/direct 成功，cr_created=PR 已开，cr_failed=开 PR 失败
	// （分支已推可手动补），apply_failed/commit_failed/push_failed=各步失败。
	fixPush = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_fix_push_total", Help: "修复推送环结果分布（阶段 3 有效率分子：cr_created/pushed）",
	}, []string{"mode", "result", "tenant"})

	// pollerFailures 轮询兜底链路失败计数（v3.9.2：轮询是 webhook 不可达环境的
	// 主力任务源，此前失败仅 Warn 日志，API 故障率/截断率无法聚合告警）。
	pollerFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_poller_failures_total", Help: "轮询兜底失败分布（list_failed/page_truncated/task_create_failed）",
	}, []string{"reason", "tenant"})

	// commentBusy 评论链路并发闸满载拒绝（评论风暴 / 容量不足信号，按租户）。
	commentBusy = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_busy_total", Help: "评论链路并发闸满载拒绝次数（调用方收到 busy；按租户）",
	}, []string{"tenant"})

	// commentPostFailures 评论回帖失败计数（问了没人回的可观测面）。
	commentPostFailures = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_post_failures_total", Help: "评论回帖失败分布（reply/…）",
	}, []string{"kind", "tenant"})

	// bestOfN best-of-N 采样结果分布（result: full|partial；成本 ×N 的对照组观测）。
	bestOfN = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_plugin_bestofn_total", Help: "best-of-N 采样结果分布（v3.10.0）",
	}, []string{"plugin", "result", "tenant"})

	// ---- 第三轮审计 P2-05（#16）：运行时可靠性指标五枚 ----
	// 迁移租约失租（P1-01 fencing 触发：续约失败/被夺/终检失败）——频繁非零 =
	// 多副本迁移锁竞争或 DB 连接不稳，需人工核查（联动 P0-04 迁移租约串行）。
	// 基础设施面，刻意不带租户维度。
	migrationLeaseLost = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "argus_migration_lease_lost_total", Help: "迁移租约失租次数（fencing 取消迁移执行；P1-01）",
	})
	// 授权拒绝（P0-02 统一 Authorizer）：route×scope 维度——泄露尝试/误配 token
	// 的可观测面（authz_denied 频发 = 约束外扫描或 token 配置漂移）。
	authzDenied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_authz_denied_total", Help: "授权拒绝计数（统一 Authorizer；P0-02）",
	}, []string{"route", "scope", "tenant"})
	// 事件丢弃（P1-05 事件面）：fanout 环回去重不计（正常形态），SSE 授权过滤、
	// outbox 落库失败、Bus 满载丢弃才计——非零需查事件链路健康。
	eventsDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_events_dropped_total", Help: "事件丢弃计数（授权过滤/outbox 失败/Bus 满载；P1-05）",
	}, []string{"reason", "tenant"})
	// 重复回帖（P2-03 副作用账本）：平台 at-least-once 重投递命中幂等账本计数
	// ——持续非零 = webhook 重试风暴或对端异常。
	commentRedelivery = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_comment_redelivery_total", Help: "评论投递幂等命中计数（重复投递；P2-03）",
	}, []string{"tenant"})
	// 预算超限（P1-06 双账本）：ledger=quota（done 口径月度配额，任务失败终态）/
	// real（真实消费账，新任务暂缓）。
	budgetExceeded = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_budget_exceeded_total", Help: "预算超限计数（quota=配额账 real=真实消费账；P1-06）",
	}, []string{"ledger", "tenant"})
	// 副作用不一致（P2-03 对账面）：任务终态与回帖副作用错位（回帖成功但任务
	// failed / 任务 done 但确认回帖缺失）——运维 ticker 对账窗口内计数。
	sideEffectMismatch = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_side_effect_mismatch_total", Help: "任务终态与回帖副作用不一致检出（P2-03 对账）",
	}, []string{"tenant"})
	// 副作用闸 fail-soft 直通：存储不可用/开关关闭/runner 未装配时直接施加，
	// **不承诺 at-most-once**——非零需要查存储或装配面（C1 观测边界）。
	sideEffectUnavailable = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_sideeffect_store_unavailable_total",
		Help: "副作用闸 fail-soft 直通计数（reason=mode_off/no_runner/store_unavailable）",
	}, []string{"reason", "tenant"})
	// 未知态副作用自动对账结果（#43）：result=applied（确认已生效）/
	// retryable（确认未生效、重新可执行）/skipped（Key 类型不支持）/
	// error（查询或裁决失败，下轮重试）。skipped/error 持续非零需查支持面。
	sideEffectReconcile = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "argus_sideeffect_reconcile_total",
		Help: "未知态副作用自动对账结果计数（result=applied/retryable/skipped/error；#43）",
	}, []string{"result", "tenant"})
)

// Init 把指标注册进 oekit/metrics Registry 并接通 plugin 观测钩子。幂等。
// metrics.Init 补齐 go/process/buildinfo 通用收集器（此前 /metrics 只有 argus_* 指标）。
//
// 钩子签名（#110）：首参 tenant 由 plugin 包 Report* 从 ctx（tenant.FromContext）
// 提取后传入——obs 不 import tenant，提取单源在 plugin。
func Init() {
	metrics.Init("argus")
	metrics.Registry.MustRegister(
		tasksTotal, stageDuration, pluginDuration, pluginFailures, llmTokens, findingsTotal,
		llmCachedTokens, llmPromptTokens,
		findingsByRepo, taskDurationByRepo,
		tasksPending, tasksRunning, notifyFailures, reflectDropped, reflectDegraded, layerExec,
		ctxFit, llmContinued, toolAnomaly, ctxBreakdown, r2SoftFailure,
		verdicts, fenceNeutralized, pluginSkipped, commentRoutes, taskDeduped, sseConnections,
		webhookSkipped, webhookRejected, webhookEnsure, llmFailovers, qaFailures, testGenVerify,
		fixVerify, fixPush,
		pollerFailures, commentBusy, commentPostFailures, bestOfN,
		migrationLeaseLost, authzDenied, eventsDropped, commentRedelivery, budgetExceeded, sideEffectMismatch,
		sideEffectUnavailable, sideEffectReconcile,
		evalMatchRatio, evalCasesTotal, evalCasesPassed, evalResultsTimestamp,
	)
	plugin.HookTaskTotal = func(tenant, platform, kind, status string) {
		tasksTotal.WithLabelValues(platform, kind, status, tenant).Inc()
	}
	plugin.HookStageDuration = func(tenant, stage string, d time.Duration) {
		stageDuration.WithLabelValues(stage, tenant).Observe(d.Seconds())
	}
	plugin.HookPluginDuration = func(tenant, name string, d time.Duration) {
		pluginDuration.WithLabelValues(name, tenant).Observe(d.Seconds())
	}
	plugin.HookPluginFailure = func(tenant, name string) {
		pluginFailures.WithLabelValues(name, tenant).Inc()
	}
	plugin.HookLLMTokens = func(tenant, stage, model string, n int) {
		llmTokens.WithLabelValues(stage, model, tenant).Add(float64(n))
	}
	plugin.HookLLMCache = func(tenant, model string, cached, prompt int) {
		llmCachedTokens.WithLabelValues(model, tenant).Add(float64(cached))
		llmPromptTokens.WithLabelValues(model, tenant).Add(float64(prompt))
	}
	plugin.HookFindings = func(tenant, severity, name string, n int) {
		findingsTotal.WithLabelValues(severity, name, tenant).Add(float64(n))
	}
	plugin.HookFindingsByRepo = func(tenant, platform, repo, severity string, n int) {
		findingsByRepo.WithLabelValues(platform, repo, severity, tenant).Add(float64(n))
	}
	plugin.HookTaskDurationByRepo = func(tenant, platform, repo, kind string, seconds float64) {
		taskDurationByRepo.WithLabelValues(platform, repo, kind, tenant).Observe(seconds)
	}
	plugin.HookReflectDrop = func(tenant, reason string) {
		reflectDropped.WithLabelValues(reason, tenant).Inc()
	}
	plugin.HookReflectDegrade = func(tenant, reason string) {
		reflectDegraded.WithLabelValues(reason, tenant).Inc()
	}
	plugin.HookLayerExec = func(tenant, layer string) {
		layerExec.WithLabelValues(layer, tenant).Inc()
	}
	plugin.HookVerdict = func(tenant, platform, kind, verdict, rule string) {
		verdicts.WithLabelValues(platform, kind, verdict, rule, tenant).Inc()
	}
	plugin.HookFenceNeutralized = func(tenant string, n int) {
		fenceNeutralized.WithLabelValues(tenant).Add(float64(n))
	}
	plugin.HookPluginSkipped = func(tenant, reason string) {
		pluginSkipped.WithLabelValues(reason, tenant).Inc()
	}
	plugin.HookCommentRoute = func(tenant, route, via string) {
		commentRoutes.WithLabelValues(route, via, tenant).Inc()
	}
	plugin.HookTaskDeduped = func(tenant, reason string) {
		taskDeduped.WithLabelValues(reason, tenant).Inc()
	}
	plugin.HookBestOfN = func(tenant, name, result string) {
		bestOfN.WithLabelValues(name, result, tenant).Inc()
	}
	// 迁移租约失租钩（P2-05，#16）：storage 不 import obs（arch 纪律），钩子注入
	// 与 plugin.Hook* 同模式。基础设施面，不带租户。
	storage.HookMigrationLeaseLost = func() {
		migrationLeaseLost.Inc()
	}
}
