// runner.go —— Runner 结构与任务执行主面：Execute/run 主流程与报告组装
// （包文档见 doc.go；生命周期子步骤见 runner_lifecycle.go，失败收口见
// runner_failure.go）。

package runner

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"

	"github.com/oagentkit/obs/audit"
	"github.com/oekit/concurrency/ratelimit"
	"github.com/oekit/logx"

	"github.com/oagentkit/llm"
	"github.com/oagentkit/rag"

	oagentprogress "github.com/oagentkit/runtime/progress"
	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/dispatch"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/reportview"
	"github.com/yi-nology/argus/internal/sink"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/toolpool"
	"github.com/yi-nology/argus/internal/workflow/requirement"

	"github.com/oagentkit/skillkit/skill"
	"github.com/oagentkit/tools/workcopy"
)

// Runner 单任务执行器。
type Runner struct {
	Cfg       *config.Config
	Store     *storage.Store
	SDK       *argusctx.SDKCtx
	Model     model.BaseChatModel // eino 模型（每任务包一层 llm.Client 以隔离预算）
	ModelName string
	// TenantStacks 租户模型视图表（#95）：bootstrap 装配 TenantProfiles LLM
	// 覆盖的租户；nil = 单租户形态。modelBundle(ctx) 按 ctx 租户查表。
	TenantStacks map[string]*ModelBundle
	// 默认视图惰性快照（defBundleOnce/defBundle，model_bundle 单源维护）。
	defBundleOnce sync.Once
	defBundle     *ModelBundle

	// LLM 装配期构造的生成器（*llm.Client 或 *llm.Resilient）。非 nil 时优先于 Model。
	LLM        llm.Generator
	Dispatcher *dispatch.Dispatcher
	Merger     *dispatch.Merger
	ToolPool   toolpool.Pool
	ToolSpecs  []toolpool.ToolSpec
	Knowledge  rag.KnowledgeService
	// WC 工作副本池（OCR delegate 前置使用；nil = 委托前置不生效）。
	WC *workcopy.Pool
	// TenantWCPools 租户工作副本池（#81）：bootstrap 为 TenantProfiles 配了
	// 平台凭证覆盖的租户在 <实例根>/t-<tenant> 下建独立池——克隆目录与凭证
	// 指纹按租户分桶，同坐标跨租户不再共享工作副本。nil = 单租户形态。
	TenantWCPools map[string]*workcopy.Pool
	Bus           *oagentprogress.Bus[progress.Event] // 任务进度事件（v3.1 SSE；nil 安全）
	EventSeen     *progress.Dedup                     // 实例级事件去重环（v6.12 P1：本地发布登记 + fanout 回灌查环；nil = 旧语义）
	Notify        *notify.Notifier                    // 终态 IM 推送（v3.1 octopus；nil 安全）
	Sink          *sink.Reporter                      // 审查结果上报（ARGUS_SINK_URL 非空启用；nil 安全，失败只告警）
	Poster        *poster.Poster
	Log           logx.Logger
	Version       string
	// Audit 审计日志（v3.86.0：verdict.posted / fix.push 等关键动作留痕；
	// oagentkit/obs/audit nil 接收者安全，未注入 = 只少审计不报错）。
	Audit *audit.Logger
	// Effects 创建型副作用闸（C1）：修复 PR 创建（2min TTL）。
	// nil = 按 Store + ARGUS_SIDEEFFECT_MODE 惰性装配。
	Effects *effect.Runner
	// PushEffects 分支推送副作用闸（C1；5min TTL）。nil = 惰性装配。
	PushEffects *effect.Runner

	// LLMLimiter 全局 LLM 请求限速器（nil = 不限速）。
	LLMLimiter ratelimit.Limiter

	// Cost 全局成本追踪器（obsx OnUsage 回流；nil = 不记账）。
	Cost *llm.CostTracker
	// CostPer1K 模型 → 单价换算（主/备模型外的调用按 0 计）。
	CostPer1K func(model string) [2]float64

	// StageLLMs 阶段模型覆盖（P6：ARGUS_MODEL_R1/R2；经 StageRouter 进 workflow，
	// 未覆盖阶段走缺省链）。generator 为装配期原样实例，预算在包装时注入。
	StageLLMs map[string]llm.Generator
	// R3Model R3 ReAct 专用模型覆盖（P6：ARGUS_MODEL_R3；nil = 主模型）。
	// 已由 main 侧 Failover 装饰时直接使用；nil 时 buildWorkflow 用 LLM.RawModel 再包。
	R3Model model.BaseChatModel
	// FallbackModel ReAct 主路径 failover 备模型（oagentkit llm.FailoverModel；
	// nil = 不启用）。Resilient.RawModel 只返回主模型，ReAct 旁路靠本字段补齐降级。
	FallbackModel model.BaseChatModel
	FallbackName  string
	// ReflectRounds R3.5 报告反思轮数（v3.4：ARGUS_REFLECTION_ROUNDS；0 = 关闭）。
	ReflectRounds int
	// ReflectModel R3.5 反思专用模型（ARGUS_MODEL_R35；nil = 回退 R3Model → 主模型）。
	ReflectModel model.BaseChatModel
	// ReflectEvidence 证据门模式（ARGUS_REFLECTION_EVIDENCE：off|soft|strict，默认 off）。
	ReflectEvidence string

	// G4 完备性裁判（v3.11.0）：annotate|demote（ARGUS_COMPLETENESS_POLICY）；
	// Retry=受限补审开关（ARGUS_COMPLETENESS_RETRY，默认关）。
	CompletenessPolicy string
	CompletenessRetry  bool
	// Learning 模式级后悔学习环（ARGUS_REGRET_LEARNING，默认关闭）。
	Learning learning.Config

	// MemKernel 评审记忆内核（v3.151.0 引入；v5.0.0 起记忆唯一路径）：
	// 非 nil 时 R3 记忆注入走内核八段召回 + MemoryBand 防伪包裹。
	MemKernel *memorykernel.Kernel

	// R3 方法论注入（requirement-compliance 插件的 skillRefs，装配期传入）
	SkillProvider skill.Provider
	SkillRefs     []string

	skillMu      sync.Mutex
	skillCache   string
	skillLoaded  bool
	skillRefresh time.Time // 下次刷新时间（10 分钟周期，与 RAG rescan 对齐）
}

// Execute 执行一个已 claim（running）的任务。正常路径始终终结任务状态（done/failed/stale）；
// 停机硬取消例外（保持 running，心跳过期后重置回 pending）。
// Phase1①：预算闸提到 Budget 创建之前——任务级 LLM Budget 用闸的授予值
// （granted）而非原始 knobs.taskTokenBudget，账面预留与执行上限一致。
func (r *Runner) Execute(ctx context.Context, taskID string) error {
	return execute(r, ctx, taskID)
}

// guardPanic 任务体 panic → error 的统一安全出口（v3.185.4）。上游
// queue/worker 的 runTask 对 panic 只记一行日志（无栈、无终态），依赖心跳
// 过期复位——确定性 panic 下等于无限「复位→再炸」。本守卫把 panic 转成
// error 交回调用方走 handleRunFailure 的 failed-retry 退避正道，并补全栈
// 日志（此前排查影响面注入 panic 被迫临时 defer 才拿到栈）。
func guardPanic(log logx.Logger, taskID string, body func() error) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("任务执行 panic: %v", rec)
			log.Error("argus.runner.task_panic", "task", taskID,
				"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
		}
	}()
	return body()
}

// run 单任务管线执行体。v3.58.0 拆分为四个命名子步骤（顺序即语义）：
// prepareContext（PR 上下文/仓库配置/diff/stale 判定）→ buildPluginTask
// （任务组装 + checkpoint + 各注入）→ recordRegret（学习环记账）→
// finalizeAndAnnounce（终态落库/sink/marker/播报）。此前 202 行单体是
// 勘察在案的可读性债（finish/r3Tools 同款清偿）。
func (r *Runner) run(ctx context.Context, task *storage.Task, wf *requirement.Workflow,
	budget *llm.Budget, start time.Time, taskCost *llm.CostTracker, knobs taskKnobs) error {
	return run(r, ctx, task, wf, budget, start, taskCost, knobs)
}

// applyTaskReportTemplate v6.11.0 报告模板四级裁决：任务级（commentcmd
// --template / API 注入）优先——须 IsValid 复核（API 建任务可绕开 commentcmd
// 词表校验，非法值告警弃用）；任务级缺席时落 repo_settings admin > 仓 yaml >
// env 链。R4 builtin 适配器与 workflow 综合段都经此 Artifacts 键消费。
func (r *Runner) applyTaskReportTemplate(ptask *plugin.Task, task *storage.Task,
	rc *plugin.RepoConfig, knobs taskKnobs) {
	applyTaskReportTemplate(r, ptask, task, rc, knobs)
}

// assembleReport 报告输入组装的 run 侧收口：buildReport 骨架 + 结论行/规则 +
// 规模统计 + CI 状态 + 影响面折叠段 + 行内评论上限（模板渲染交给 reportview）。
func (r *Runner) assembleReport(task *storage.Task, pr *plugin.PRContext, ptask plugin.Task,
	out *plugin.WorkflowOutput, taskCost *llm.CostTracker, rc *plugin.RepoConfig) reportview.Report {
	return assembleReport(r, task, pr, ptask, out, taskCost, rc)
}

// prepareContext PR 上下文与前置判定：平台 API 组装 → 仓库主权配置单次加载 →
