package plugin

import (
	"context"
	"time"

	"github.com/oagentkit/agent/agentrun"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/tenant"
)

// 观测钩子：本包不直接依赖 prometheus，由 obs 包在装配期注入实现（design §10.1）。
// 保持 L4 纯逻辑、可在无指标环境（单测）下运行。
//
// 租户维度（#110）：Hook* 首参 tenant；Report*/Obs* 首参 ctx 并在此用
// tenant.FromContext 提取（nil ctx 回落 default，nil 安全）。提取单源在本包——
// obs 不 import tenant，workflow/dispatch 消费方也无需各自提取。
var (
	// HookTaskTotal 任务终态计数（tenant, platform, kind, status）。
	HookTaskTotal func(tenant, platform, kind, status string)
	// HookStageDuration 阶段耗时（tenant, stage: R1..R5 | plugin:<name> | total）。
	HookStageDuration func(tenant, stage string, d time.Duration)
	// HookPluginDuration 插件耗时。
	HookPluginDuration func(tenant, plugin string, d time.Duration)
	// HookPluginFailure 插件失败计数（含 poster）。
	HookPluginFailure func(tenant, plugin string)
	// HookLLMTokens LLM token 消耗（tenant, stage, model）——只做观测，不参与预算判定。
	HookLLMTokens func(tenant, stage, model string, tokens int)
	// HookLLMCache LLM prompt 缓存命中记账（tenant, model, cached, prompt；cached ⊆ prompt）。
	// 前缀缓存命中率 = cached/prompt，命中 token 计价折扣见 Config.CacheReadRatio。
	HookLLMCache func(tenant, model string, cached, prompt int)
	// HookFindings 审查产出分布（tenant, severity, plugin）。
	HookFindings func(tenant, severity, plugin string, n int)
	// HookFindingsByRepo 按仓 findings 趋势（v3.73.0，repo=owner/name，severity）。
	// 试点/企业内部环境 repo 基数可控；公共多租户部署应评估基数。
	HookFindingsByRepo func(tenant, platform, repo, severity string, n int)
	// HookTaskDurationByRepo 按仓任务耗时趋势秒数（tenant, platform, repo, kind）。
	HookTaskDurationByRepo func(tenant, platform, repo, kind string, seconds float64)
	// HookReflectDrop R3.5 证据门 strict 丢弃。
	HookReflectDrop func(tenant, reason string)
	// HookReflectDegrade R3.5 证据门 soft 降级。
	HookReflectDegrade func(tenant, reason string)
	// HookLayerExec 分层执行探针：层被真正走到时 +1（活检；名义存在但零执行 = 告警）。
	HookLayerExec func(tenant, layer string)
	// HookVerdict 裁决分布（tenant, platform, kind, verdict, rule）——R5 真值表命中分布。
	HookVerdict func(tenant, platform, kind, verdict, rule string)
	// HookFenceNeutralized 数据区围栏中和次数（= 检测到注入特征文本的信号量）。
	HookFenceNeutralized func(tenant string, n int)
	// HookPluginSkipped 插件跳过原因分布（circuit/budget/canceled）。
	HookPluginSkipped func(tenant, reason string)
	// HookCommentRoute 评论意图路由分布（route: review|ask, via: keyword|llm|fallback）。
	HookCommentRoute func(tenant, route, via string)
	// HookTaskDeduped 任务创建去重计数（reason: event_id|last_head_sha|in_flight）。
	HookTaskDeduped func(tenant, reason string)
	// HookBestOfN best-of-N 采样结果分布（k/n：full=全部成功、partial=部分成功）。
	HookBestOfN func(tenant, plugin, result string)
	// HookPrintUpsertFailed 指纹写入失败计数（学习/抑制账本的静默丢失信号面：
	// 生产实锤=PG 序列脱敏致新指纹永久 23505，两周无告警无指标）。
	HookPrintUpsertFailed func(tenant string)
)

// ctxTenant ctx → tenant label 值（提取单源；tenant.FromContext nil 安全）。
func ctxTenant(ctx context.Context) string { return tenant.FromContext(ctx) }

func ObsPluginDuration(ctx context.Context, name string, d time.Duration) {
	if HookPluginDuration != nil {
		HookPluginDuration(ctxTenant(ctx), name, d)
	}
}

func obsLLMTokens(t, stage, model string, tokens int) {
	if HookLLMTokens != nil {
		HookLLMTokens(t, stage, model, tokens)
	}
}

func ObsFindings(ctx context.Context, severity, name string, n int) {
	if HookFindings != nil && n > 0 {
		HookFindings(ctxTenant(ctx), severity, name, n)
	}
}

// ReportLLMTokens 供 llm 包记账（argus_llm_tokens_total，只观测不判定）。
func ReportLLMTokens(ctx context.Context, stage, model string, tokens int) {
	if tokens > 0 {
		obsLLMTokens(ctxTenant(ctx), stage, model, tokens)
	}
}

// ReportLLMCache 供 llm 记账回调上报 prompt 缓存命中（model, cached, prompt；cached ⊆ prompt）。
// 前缀缓存命中率 = cached/prompt；cached 只记观测，不影响预算判定。
func ReportLLMCache(ctx context.Context, model string, cached, prompt int) {
	if HookLLMCache != nil && (cached > 0 || prompt > 0) {
		HookLLMCache(ctxTenant(ctx), model, cached, prompt)
	}
}

// ReportPluginFailure 插件失败计数（argus_plugin_failures_total，含 poster 等非插件路径）。
func ReportPluginFailure(ctx context.Context, name string) {
	if HookPluginFailure != nil {
		HookPluginFailure(ctxTenant(ctx), name)
	}
}

// ReportTaskTotal 供 runner 记任务终态。
func ReportTaskTotal(ctx context.Context, platform, kind, status string) {
	if HookTaskTotal != nil {
		HookTaskTotal(ctxTenant(ctx), platform, kind, status)
	}
}

// ReportFindingsByRepo 按仓 findings 埋点（nil 钩子安全）。
func ReportFindingsByRepo(ctx context.Context, platform, repo, severity string, n int) {
	if HookFindingsByRepo != nil {
		HookFindingsByRepo(ctxTenant(ctx), platform, repo, severity, n)
	}
}

// ReportTaskDurationByRepo 按仓耗时埋点（nil 钩子安全）。
func ReportTaskDurationByRepo(ctx context.Context, platform, repo, kind string, seconds float64) {
	if HookTaskDurationByRepo != nil {
		HookTaskDurationByRepo(ctxTenant(ctx), platform, repo, kind, seconds)
	}
}

// ReportStageDuration 供 runner 记阶段耗时。
func ReportStageDuration(ctx context.Context, stage string, d time.Duration) {
	if HookStageDuration != nil {
		HookStageDuration(ctxTenant(ctx), stage, d)
	}
}

// ReportReflectDrop R3.5 证据门 strict 丢弃（reason=missing_evidence|missing_counterexample|evidence_path_out_of_scope）。
func ReportReflectDrop(ctx context.Context, reason string) {
	if HookReflectDrop != nil {
		HookReflectDrop(ctxTenant(ctx), reason)
	}
}

// ReportReflectDegrade R3.5 证据门 soft 降级。
func ReportReflectDegrade(ctx context.Context, reason string) {
	if HookReflectDegrade != nil {
		HookReflectDegrade(ctxTenant(ctx), reason)
	}
}

// ReportLayerExec 分层活检：层实际执行时记一次。
// 词表钉死：diffprep | R1 | R2 | R3 | R35 | R4 | R5 | merger | poster。
func ReportLayerExec(ctx context.Context, layer string) {
	if HookLayerExec != nil && layer != "" {
		HookLayerExec(ctxTenant(ctx), layer)
	}
}

// ReportVerdict R5 裁决分布（argus_verdicts_total）。
func ReportVerdict(ctx context.Context, platform, kind, verdict, rule string) {
	if HookVerdict != nil {
		HookVerdict(ctxTenant(ctx), platform, kind, verdict, rule)
	}
}

// ReportFenceNeutralized 围栏中和计数（注入特征信号，argus_fence_neutralized_total）。
func ReportFenceNeutralized(ctx context.Context, n int) {
	if HookFenceNeutralized != nil && n > 0 {
		HookFenceNeutralized(ctxTenant(ctx), n)
	}
}

// ReportPluginSkipped 插件跳过原因（reason 词表：circuit | budget | canceled）。
func ReportPluginSkipped(ctx context.Context, reason string) {
	if HookPluginSkipped != nil && reason != "" {
		HookPluginSkipped(ctxTenant(ctx), reason)
	}
}

// ReportCommentRoute 评论意图路由分布。
func ReportCommentRoute(ctx context.Context, route, via string) {
	if HookCommentRoute != nil {
		HookCommentRoute(ctxTenant(ctx), route, via)
	}
}

// ReportTaskDeduped 任务创建去重（reason 词表：event_id | last_head_sha | in_flight）。
func ReportTaskDeduped(ctx context.Context, reason string) {
	if HookTaskDeduped != nil {
		HookTaskDeduped(ctxTenant(ctx), reason)
	}
}

// ReportBestOfN best-of-N 采样结果分布（result 词表：full | partial）。
func ReportBestOfN(ctx context.Context, name, result string) {
	if HookBestOfN != nil {
		HookBestOfN(ctxTenant(ctx), name, result)
	}
}

// ReportPrintUpsertFailed 指纹写入失败（学习/抑制账本丢失信号）。
func ReportPrintUpsertFailed(ctx context.Context) {
	if HookPrintUpsertFailed != nil {
		HookPrintUpsertFailed(ctxTenant(ctx))
	}
}

// Layer 管线阶段词表（单一出处，v3.46.0 契约显式化）。所有跨包消费阶段名之处
// 一律引用此处常量，禁止裸字符串——新增阶段只需在此追加并扩展 StageTimeline：
//   - 指标 label：ReportStageDuration/ReportLayerExec（与告警规则 label 对齐，改值断 Grafana）；
//   - SSE 进度事件：Workflow.onStage；
//   - LLM 阶段路由键：llm.StageRouter 前缀匹配（LayerR1 命中 LayerR1A），模型覆盖/包装同键；
//   - MCP TaskStages 阶段时间线：service 按 StageTimeline 顺序出表。
const (
	LayerDiffprep = "diffprep"
	LayerR1       = "R1"
	LayerR1A      = "R1a" // R1 的分块提炼子阶段（StageRouter 由 LayerR1 前缀命中）
	LayerR2       = "R2"
	LayerR3       = "R3"
	LayerR35      = "R35"
	LayerR4       = "R4"
	LayerR5       = "R5"
	// LayerR4X 分析师综合段（v6.10.0 报告契约模板）：merge 后按仓模板的条件性
	// 附加调用。指标/token 记账 label 用；不进 StageTimeline（时间线是用户可见
	// 审查进度，综合段是模板附加成本不显示进度槽位）。StageRouter 前缀命中
	// LayerR4——R4 模型覆盖/包装对综合段同样生效（刻意：同一审查任务的综合
	// 质量与专家审查同档模型）。
	LayerR4X    = "R4x"
	LayerMerger = "merger"
	LayerPoster = "poster"
)

// StageTimeline 阶段时间线的规范顺序（只读词表，消费方勿改写元素；新增阶段在此追加）。
var StageTimeline = []string{LayerR1, LayerR2, LayerR3, LayerR35, LayerR4, LayerR5}

// ToolTraceLogger agentrun 工具轨迹审计日志（tool_result 只落长度不落内容，防
// 敏感代码入库；v3.64.0 自 builtin/stages 两处克隆收口）。prefix 决定事件名
// （<prefix>.tool_call / .tool_result），kv 为附加固定键值（如 plugin 名）。
// log 为 nil 时返回 nil 闭包（agentrun 侧 nil 安全）。
func ToolTraceLogger(log logx.Logger, prefix string, kv ...any) func(agentrun.Event) {
	if log == nil {
		return nil
	}
	return func(e agentrun.Event) {
		switch e.Type {
		case agentrun.EventToolCall:
			log.Info(prefix+".tool_call", append(kv, "tool", e.Tool)...)
		case agentrun.EventToolResult:
			log.Info(prefix+".tool_result", append(kv, "tool", e.Tool, "bytes", len(e.Text))...)
		}
	}
}
