package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/obs/audit"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/runner"
	"github.com/yi-nology/argus/internal/service"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/toolpool"
)

// buildModelKnowledgeStack 模型栈（主模型 + 全局降级 + P6 阶段覆盖，见 models.go）
// + L5 知识子系统（Milvus 向量检索 > 本地 RAG > Noop）
// + L1 扩展：进度事件总线 + octopus IM 推送（v3.1）。
func buildModelKnowledgeStack(ctx context.Context, cfg *config.Config, log logx.Logger, sys *System) error {
	models, err := BuildModels(ctx, cfg, log)
	if err != nil {
		return err
	}
	sys.Models = models
	sys.Knowledge = buildKnowledge(ctx, cfg, log)
	sys.Bus = oagentprogress.NewBus[progress.Event]()
	// v6.12 P1：实例级事件去重环（同实例 at-most-once）——Bus 广播与 outbox
	// 回灌双源经共享环去重；Service 与 Runner 注入同一实例。
	sys.EventSeen = progress.NewDedup(0)
	if cfg.OctopusURL != "" {
		sys.Notify = &notify.Notifier{
			BaseURL: cfg.OctopusURL, ChannelID: cfg.OctopusChannelID,
			User: cfg.OctopusUser, Token: cfg.OctopusToken, Log: log,
		}
		// 租户路由（#97）：profile 配了 octopus 覆盖的租户逐条登记——渠道 ID
		// 继承全局（profile TenantEndpoint 暂无渠道字段，v1 语义）。
		for tid, prof := range cfg.TenantProfiles {
			if prof.Octopus.URL == "" {
				continue
			}
			if sys.Notify.Routes == nil {
				sys.Notify.Routes = map[string]notify.TenantRoute{}
			}
			sys.Notify.Routes[tid] = notify.TenantRoute{
				BaseURL: prof.Octopus.URL, ChannelID: cfg.OctopusChannelID, Token: prof.Octopus.Token,
			}
		}
		log.Info("argus.boot.notify_ready", "octopus", cfg.OctopusURL, "channel", cfg.OctopusChannelID)
	}
	return nil
}

// buildPluginRegistry 插件 registry 构建（L4 插拔层 + L3 编排入口）。
// runnerRef 以双指针传入：闭包捕获的是调用方的指针变量，Assemble 后半段
// 回填 runner 时闭包即可见（延迟解引用语义与内联版一致）。
func buildPluginRegistry(cfg *config.Config, log logx.Logger, sys *System,
	pf *plugin.PluginsFile, models *Models, runnerRef **runner.Runner) (*dispatch.Registry, []string, error) {
	reg, complianceSkillRefs, err := BuildRegistry(&Deps{
		Cfg: cfg, Log: log, WC: sys.WC, Model: models.React, Skills: sys.Skills, Plugins: pf,
		WorkflowFactory: func(budget *llm.Budget) plugin.WorkflowRunner {
			if *runnerRef == nil {
				return nil // 装配完成前不该被调用；调用方 panic=明确报错
			}
			return (*runnerRef).WorkflowFactory()(budget)
		},
		// R4 builtin ReAct 化：Reviewer 复用 R3 工具基础设施（get_file/get_diff/
		// 知识/技能/MCP），失败自动落回单轮调用
		ReviewTools: func(ctx context.Context, task plugin.Task, budget *llm.Budget) ([]tool.BaseTool, string, error) {
			if *runnerRef == nil {
				return nil, "", fmt.Errorf("runner 未就绪")
			}
			wf := (*runnerRef).WorkflowFor(budget)
			// 仓库级评审规范（v3.38.0）：R4 与 R3 同源装载
			wf.RepoGuide = (*runnerRef).RepoGuideFor(ctx, task.PR.Platform, task.PR.Owner, task.PR.Repo, task.PR.HeadSHA)
			return wf.ReviewTools(ctx, task)
		},
		ReviewerLLM:  models.ReviewerGen,
		ReviewerName: models.ReviewerName,
		ReactModel:   models.ReviewerReact,
	})
	if err != nil {
		return nil, nil, err
	}
	return reg, complianceSkillRefs, nil
}

// buildToolPool MCP server → eino 工具表（design §9.2 方向 A 实装）。
// plugins.yaml 的 mcpServers/tools 段驱动；未声明时 nil（workflow 不挂工具）。
func buildToolPool(cfg *config.Config, pf *plugin.PluginsFile, log logx.Logger) (toolpool.Pool, []toolpool.ToolSpec) {
	var toolPool toolpool.Pool
	var toolSpecs []toolpool.ToolSpec
	if len(pf.MCPServers) > 0 {
		if rp := toolpool.NewRealPool(pf.MCPServers, log); rp != nil {
			// 闲置连接回收（ARGUS_MCP_IDLE_REAP_MAX_IDLE > 0 启用）：长驻池的
			// 闲置 stdio/HTTP 连接周期性摘除，重连在下次取工具时按需发生。
			if cfg.MCPIdleReapMaxIdle > 0 {
				rp.StartIdleReaper(cfg.MCPIdleReapInterval, cfg.MCPIdleReapMaxIdle)
				log.Info("argus.boot.mcp_idle_reaper", "interval", cfg.MCPIdleReapInterval.String(),
					"max_idle", cfg.MCPIdleReapMaxIdle.String())
			} else if cfg.MCPIdleReapInterval != 5*time.Minute {
				// 配置陷阱如实化（v3.41.0）：单配 INTERVAL 不启用回收
				log.Warn("argus.boot.mcp_idle_reaper_ignored", "interval_configured", cfg.MCPIdleReapInterval.String(),
					"hint", "仅配置 ARGUS_MCP_IDLE_REAP_INTERVAL 不启用回收；需 ARGUS_MCP_IDLE_REAP_MAX_IDLE>0")
			}
			toolPool = &toolpool.RealAdapter{Pool: rp}
			toolSpecs = toolpool.SpecsFromDecls(pf.Tools)
			log.Info("argus.boot.mcp_ready", "servers", strings.Join(rp.Servers(), ","),
				"tool_specs", len(toolSpecs))
		}
	}
	return toolPool, toolSpecs
}

// buildOrchestration 裁决/合并/回帖装配 + 审计外送 logger 包装。
// 返回 argus-audit 审计器（runner 与 service 共用，v3.86.0）。
func buildOrchestration(cfg *config.Config, log logx.Logger, store *storage.Store,
	sdk *argusctx.SDKCtx, reg *dispatch.Registry, squads []plugin.SquadDecl,
	version string, sys *System) *audit.Logger {
	dispatcher := dispatch.NewDispatcher(reg, log).WithSquads(squads)
	dispatcher.MaxConcurrent = cfg.PluginMaxConcurrent
	merger := dispatch.NewMerger(store, log)
	if cfg.RegretLearning {
		merger.WithLearning(store, cfg.LearningConfig())
		log.Info("argus.regret_learning.enabled",
			"min_samples", cfg.RegretMinSamples,
			"soft_floor", cfg.RegretSoftFloor,
			"hard_floor", cfg.RegretHardFloor)
	}
	sys.Dispatcher = dispatcher
	sys.Merger = merger
	sys.Poster = &poster.Poster{SDK: sdk, Store: store, Version: version, BotUsername: cfg.BotUsername, Log: log}
	// 审计外送（v3.137.0，SIEM 对接旁路）：URL 配置时包一层转发 logger，
	// audit.event 本地留痕之外再 POST JSON 一份；未配置 = 原样。
	auditLoggerIn := log
	if cfg.AuditWebhookURL != "" {
		fwd := service.NewAuditForwarder(cfg.AuditWebhookURL, cfg.AuditWebhookToken, log)
		// 租户路由（#97）：profile 配了 audit_webhook 覆盖的租户逐条登记。
		for tid, prof := range cfg.TenantProfiles {
			if prof.AuditWebhook.URL != "" {
				fwd.Routes[tid] = service.TenantRoute{URL: prof.AuditWebhook.URL, Token: prof.AuditWebhook.Token}
			}
		}
		auditLoggerIn = fwd.Wrap(log)
		log.Info("argus.boot.audit_forward_enabled", "endpoint_set", true)
	}
	return audit.New(auditLoggerIn, "argus-audit")
}

// buildRunner 执行编排 runner 构造（L3 编排 + 结果链路的汇聚点）。
func buildRunner(cfg *config.Config, log logx.Logger, store *storage.Store, sdk *argusctx.SDKCtx,
	models *Models, auditLog *audit.Logger, complianceSkillRefs []string,
	version string, sys *System) *runner.Runner {
	// ---- LLM 成本记账（obsx OnUsage 回流，覆盖直连与 ReAct RawModel 两条路径）----
	costTracker := llm.NewCostTracker()
	costRates := cfg.CostPer1K()
	// v3.36.0：未知模型（阶段覆盖）按主费率近似记账——此前恒按 0，成本行系统性
	// 低估（v3.8.0 告警在案解除）；同厂兼容端点近似同价，误差远小于 0。
	costPer1K := func(mdl string) [2]float64 {
		return costRates
	}
	r := &runner.Runner{
		Cfg: cfg, Store: store, SDK: sdk,
		Model: models.React, ModelName: cfg.LLM.Model,
		LLM:        models.Generate,
		ToolPool:   sys.ToolPool,
		ToolSpecs:  sys.ToolSpecs,
		Dispatcher: sys.Dispatcher, Merger: sys.Merger,
		Poster: sys.Poster, Log: log, Version: version,
		Audit:         auditLog,
		SkillProvider: sys.Skills, SkillRefs: complianceSkillRefs,
		WC:        sys.WC, // OCR delegate 委托前置（ARGUS_OCR_BIN 启用）
		Bus:       sys.Bus,
		EventSeen: sys.EventSeen, // v6.12 P1：共享去重环（同实例 at-most-once）
		Notify:    sys.Notify,
		Knowledge: sys.Knowledge,
		// Limiter 仅作用于非降级路径的阶段覆盖 Client（历史行为，原样保留）
		LLMLimiter: models.Limiter,
		Cost:       costTracker,
		CostPer1K:  costPer1K,
		StageLLMs:  models.StageLLMs,
		R3Model:    models.R3,
		// ReAct failover：R3Model 已包时用它；nil 时 runner 用 RawModel + FallbackModel 再包
		FallbackModel: models.Fallback,
		FallbackName:  models.FallbackName,
		// R3.5 报告反思（v3.4 reflection）：轮数 0 = 关闭；模型缺省回退 R3 专用模型
		ReflectRounds:   cfg.ReflectRounds,
		ReflectModel:    models.Reflect,
		ReflectEvidence: cfg.ReflectEvidence,
		// G4 完备性裁判（v3.11.0）：annotate 默认只注明；demote 降级门；retry 受限补审
		CompletenessPolicy: cfg.CompletenessPolicy,
		CompletenessRetry:  cfg.CompletenessRetry,
		// 模式级后悔学习环（v3.5）：默认关闭；阈值单源自 cfg.LearningConfig()
		Learning: cfg.LearningConfig(),
		// 评审记忆内核（v3.151.0 引入；v5.0.0 起记忆唯一路径）
		MemKernel: sys.MemKernel,
	}
	// 租户模型栈（#95）：TenantProfiles 配了 LLM 覆盖的租户各建独立模型栈——
	// 差量合成 effective cfg 复用 BuildModels 全链（降级/阶段覆盖同纪律），
	// 失败仅告警跳过该租户（模型是增强面配置，不拖垮启动）。
	if len(cfg.TenantProfiles) > 0 {
		r.TenantStacks = map[string]*runner.ModelBundle{}
		for tid, prof := range cfg.TenantProfiles {
			if prof.LLM.BaseURL == "" && prof.LLM.APIKey == "" && prof.LLM.Model == "" {
				continue
			}
			cfgT := *cfg
			cfgT.LLM = tenantLLMEffective(cfg.LLM, prof.LLM)
			modelsT, err := BuildModels(context.Background(), &cfgT, log)
			if err != nil {
				log.Warn("argus.boot.tenant_models_failed", "tenant", tid, "error", err.Error())
				continue
			}
			r.TenantStacks[tid] = &runner.ModelBundle{
				LLM:           modelsT.Generate, // 主生成链（同全局 LLM 字段映射纪律）
				StageLLMs:     modelsT.StageLLMs,
				Model:         modelsT.React,
				ModelName:     cfgT.LLM.Model,
				R3Model:       modelsT.R3,
				FallbackModel: modelsT.Fallback,
				FallbackName:  modelsT.FallbackName,
				ReflectModel:  modelsT.Reflect,
				LLMLimiter:    modelsT.Limiter,
			}
			log.Info("argus.boot.tenant_models_ready", "tenant", tid, "model", cfgT.LLM.Model)
		}
	}
	return r
}

// tenantLLMEffective 租户 LLM 差量合成（#95）：profile 非空字段覆盖全局
// LLMConfig 对应项——profile 即差量语义，未覆盖字段继承全局（含 Timeout/
// 窗口/韧性参数，阶段模型覆盖同继承）。
func tenantLLMEffective(base config.LLMConfig, over config.TenantLLM) config.LLMConfig {
	if over.BaseURL != "" {
		base.BaseURL = over.BaseURL
	}
	if over.APIKey != "" {
		base.APIKey = over.APIKey
	}
	if over.Model != "" {
		base.Model = over.Model
	}
	return base
}
