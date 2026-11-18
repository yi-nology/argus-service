// assemble.go —— 进程装配的唯一归属（v3.5.8，自 main.go runApp 收敛）：
// SDK/工作副本/技能 → 模型栈（BuildModels）→ 知识子系统 → 总线/推送 →
// 插件注册（含 runner 延迟解引用环）→ MCP 工具表 → 成本记账 →
// dispatcher/merger/poster/runner。
// main 只保留生命周期语义：存储开关、资源注册、选主、worker 排水、HTTP。

package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/oekit/logx"

	"github.com/oagentkit/rag"
	"github.com/oagentkit/skillkit/skill"
	"github.com/oagentkit/tools/workcopy"

	oagentprogress "github.com/oagentkit/runtime/progress"
	"github.com/yi-nology/argus/internal/acpxsandbox"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/execpolicy"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/outbound"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/runner"
	"github.com/yi-nology/argus/internal/service"
	"github.com/yi-nology/argus/internal/sink"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/toolpool"
)

// System 装配产物：全部子系统就绪后的共享引用集合。
// 生命周期（Start/Stop 顺序）仍由 main 经 oekit/bootstrap 管理，本结构不做任何启停。
type System struct {
	Models    *Models
	SDK       *argusctx.SDKCtx
	WC        *workcopy.Pool
	// TenantWCPools 租户工作副本池（#81）：sys 透传 buildRunner。
	TenantWCPools map[string]*workcopy.Pool
	Skills    skill.Provider
	Knowledge rag.KnowledgeService
	Bus       *oagentprogress.Bus[progress.Event]
	EventSeen *progress.Dedup  // v6.12 P1：实例级事件去重环（Service/Runner 共享）
	Notify    *notify.Notifier // nil = 未配置 octopus

	Registry            *dispatch.Registry
	ComplianceSkillRefs []string
	ToolPool            toolpool.Pool
	ToolSpecs           []toolpool.ToolSpec
	Dispatcher          *dispatch.Dispatcher
	Merger              *dispatch.Merger
	Poster              *poster.Poster
	Runner              *runner.Runner
	Service             *service.Service
	// MemKernel agent-memory 内核底座（v3.151.0 引入；v5.0.0 起记忆唯一路径）。
	// 进程退出即关库（sqlite WAL 逐提交落盘，无需显式 Close）。
	MemKernel *memorykernel.Kernel
}

// Assemble 装配除存储/HTTP/生命周期外的全部子系统。
// store 由调用方打开后传入（merger/runner 共享）；version 供 poster/runner 留痕；
// instanceID 供 workcopy 按实例隔离工作副本目录。
func Assemble(ctx context.Context, cfg *config.Config, log logx.Logger, store *storage.Store,
	version, instanceID string) (*System, error) {
	// 出站守卫与子进程执行策略的全局配置（ARGUS_OUTBOUND_MODE/ALLOW、
	// ARGUS_EXEC_POLICY；缺省均为 warn）必须在任何出站客户端/子进程装配
	// 之前完成——execx/notify/audit/qconsole/A2A 都读各自的一份快照。
	execpolicy.ConfigureFromEnv(log)
	outbound.ConfigureFromEnv(log)
	// acpx 执行沙箱的出站声明（#37）：ARGUS_ACPX_EGRESS_MODE=unrestricted|proxy|denied
	// + PROXY/ALLOW/BYPASS。受限档缺强制后端时 agent 会在 Run 前被拒（fail closed）。
	acpxsandbox.ConfigureFromEnv(log)
	sys := &System{}

	// ---- L5 上下文：SDK 封装 + workcopy 实例隔离池 ----
	sdk, wc, tenantPools, err := initSDKAndWorkcopy(cfg, log, instanceID)
	if err != nil {
		return nil, err
	}
	sys.SDK, sys.WC = sdk, wc
	sys.TenantWCPools = tenantPools
	sys.Skills = skill.NewFileProvider(cfg.SkillsDir)

	// ---- 模型栈 + 知识子系统 + 进度总线/推送 ----
	if err := buildModelKnowledgeStack(ctx, cfg, log, sys); err != nil {
		return nil, err
	}
	models := sys.Models

	// ---- L4 插拔层：插件清单 + registry（runnerRef 延迟解引用环）----
	// 插件清单一次加载：registry 与 MCP 工具表/编队段共用（v3.5.9）。
	// 解析失败 fail-fast（深挖修复）：坏 YAML 不能静默降级为内置默认插件集。
	pf, err := loadPluginsFile(cfg, log)
	if err != nil {
		return nil, err
	}
	// runnerRef 延迟填充：requirement-compliance 插件的 factory 在 Invoke 时才解引用。
	var runnerRef *runner.Runner
	reg, complianceSkillRefs, err := buildPluginRegistry(cfg, log, sys, pf, models, &runnerRef)
	if err != nil {
		return nil, err
	}
	sys.Registry = reg
	sys.ComplianceSkillRefs = complianceSkillRefs

	// ---- L5 工具子系统：MCP server → eino 工具表（design §9.2 方向 A 实装）----
	sys.ToolPool, sys.ToolSpecs = buildToolPool(cfg, pf, log)

	// ---- 裁决/合并/回帖/执行编排 + 审计外送 ----
	auditLog := buildOrchestration(cfg, log, store, sdk, reg, pf.Squads, version, sys)

	// runner 构造后回填共享指针：延迟绑定闭包（WorkflowFactory/ReviewTools）
	// 捕获的是 &runnerRef，此赋值即闭包解引用的信号。
	runnerRef = buildRunner(cfg, log, store, sdk, models, auditLog, complianceSkillRefs, version, sys)
	// 延迟绑定不变量自检：WorkflowFactory/ReviewTools 闭包捕获 runnerRef 指针，
	// 依赖「Invoke 时装配已完成」。此处显式钉死——未来若出现条件构造路径，
	// 漏构造在此 fail-fast，而不是运行期静默拿到 nil factory。
	if runnerRef == nil {
		return nil, fmt.Errorf("bootstrap: runner 未装配（延迟绑定闭包将拿到 nil factory）")
	}
	// 结果上报 sink（ARGUS_SINK_URL 非空启用）：审查完成后 push 给 review-service
	// 承接端点（旁路，失败只告警不阻断审查主流程）。
	if cfg.SinkURL != "" {
		log.Info("argus.sink.enabled", "endpoint", cfg.SinkURL)
		runnerRef.Sink = sink.NewReporter(cfg.SinkURL, cfg.SinkToken, log)
		// 租户路由（#97）：TenantProfiles 里配了 sink 覆盖的租户逐条登记。
		for tid, prof := range cfg.TenantProfiles {
			if prof.Sink.URL != "" {
				runnerRef.Sink.Routes[tid] = sink.Route{URL: prof.Sink.URL, Token: prof.Sink.Token}
			}
		}
	}
	sys.Runner = runnerRef

	// ---- 语义检索底座 + 评审记忆内核 ----
	semMgr := buildSemSearchAndMemKernel(cfg, log, sys)

	// ---- service 门面（v3.48.0 自 main 收敛）：webhook/CRUD/Q&A/MCP 数据面 ----
	// System 由此成为完整对象图；main 只剩 HTTP 接线（SetService/Bearer/路由/停机排水）。
	sys.Service = &service.Service{
		Cfg: cfg, Store: store, SDK: sdk, Registry: reg,
		Knowledge: sys.Knowledge, Log: log, Bus: sys.Bus,
		EventSeen: sys.EventSeen, // v6.12 P1：共享去重环（同实例 at-most-once）
		Model:     models.React, QAModel: models.QA,
		LLM:         models.Generate, // diff 预审单轮写作（v3.123.0）
		RouterModel: models.Router,
		WC:          sys.WC,        // testgen 沙箱验证（ARGUS_TESTGEN_VERIFY_CMD 启用；nil 安全）
		SemSearch:   semMgr,        // search_sem 底座（ARGUS_SEMSEARCH 启用；nil 安全）
		MemKernel:   sys.MemKernel, // 评审记忆内核（v5.0.0 起记忆唯一路径）
		Audit:       auditLog, Version: version,
	}
	return sys, nil
}

// initSDKAndWorkcopy L5 上下文：SDK 封装 + workcopy 实例隔离池。
// workcopy 按实例隔离：共享 WorkRoot 时，本实例 Sweep 的孤儿扫描会把
// 他实例"已跟踪"的目录误判为孤儿删除（entries 是实例内内存态）。
func initSDKAndWorkcopy(cfg *config.Config, log logx.Logger, instanceID string) (*argusctx.SDKCtx, *workcopy.Pool, map[string]*workcopy.Pool, error) {
	sdk, err := argusctx.NewSDKCtx(cfg, log)
	if err != nil {
		return nil, nil, nil, err
	}
	wcRoot := filepath.Join(cfg.WorkRoot, "instance-"+instanceID)
	// fail-fast（v3.8.0）：目录建不出来时每个任务执行期才失败、/readyz 却照常
	// 绿灯——启动期能判定的失败不该拖到每任务撞一次。
	if err := os.MkdirAll(wcRoot, 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("工作目录创建失败 %s: %w", wcRoot, err)
	}
	wc := workcopy.NewPool(wcRoot, log)
	wc.CredentialOf = func(platform string) (string, string, bool) {
		pc, ok := cfg.Platforms[platform]
		// GitHub App 认证时，优先用 installation token（TokenSource 临期懒续期，
		// v5.3.2；取值失败回落 PAT 与未启用路径同形）
		if platform == "github" && sdk.HasGitHubApp() {
			return pc.BaseURL, sdk.GitHubAppToken(context.Background()), ok
		}
		// 写凭证分离（Phase 3）：clone/fetch 走写 token（缺省回落读 Token）——
		// 与修复链 push 同源，最小权限下只读 token 不被迫承担写。
		return pc.BaseURL, pc.WriteTokenOf(), ok
	}
	wc.InsecureTLSOf = func(platform string) bool {
		return cfg.Platforms[platform].SkipTLS
	}
	// 租户工作副本池（#81）：TenantProfiles 配了平台凭证覆盖的租户各建独立池
	//（<实例根>/t-<tenant>）——目录与凭证按租户分桶。CredentialOf 租户感知：
	// 租户在 profile 配了该平台时用租户凭证（与 sdkctx.ProviderFor 同判据），
	// 未配置回落全局（与默认池同形）。
	tenantPools := map[string]*workcopy.Pool{}
	for tid, prof := range cfg.TenantProfiles {
		hasPlatform := false
		for _, pc := range prof.Platforms {
			if pc.Token != "" {
				hasPlatform = true
				break
			}
		}
		if !hasPlatform {
			continue
		}
		tRoot := filepath.Join(wcRoot, "t-"+tid)
		if err := os.MkdirAll(tRoot, 0o755); err != nil {
			log.Warn("argus.boot.tenant_wc_failed", "tenant", tid, "error", err.Error())
			continue
		}
		tp := workcopy.NewPool(tRoot, log)
		tp.CredentialOf = func(platform string) (string, string, bool) {
			if pc, ok := prof.Platforms[platform]; ok && pc.Token != "" {
				return pc.BaseURL, pc.WriteTokenOf(), true
			}
			// 未覆盖平台回落全局凭证（租户池里审查他平台仓的兼容形态）。
			pc, ok := cfg.Platforms[platform]
			return pc.BaseURL, pc.WriteTokenOf(), ok
		}
		tp.InsecureTLSOf = func(platform string) bool {
			if pc, ok := prof.Platforms[platform]; ok {
				return pc.SkipTLS
			}
			return cfg.Platforms[platform].SkipTLS
		}
		tenantPools[tid] = tp
	}
	return sdk, wc, tenantPools, nil
}
