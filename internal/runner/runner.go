// runner.go —— Runner 结构与任务执行主面：Execute/run 主流程与报告组装
// （包文档见 doc.go；生命周期子步骤见 runner_lifecycle.go，失败收口见
// runner_failure.go）。

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
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
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/poster"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/reporttmpl"
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
	// LLM 装配期构造的生成器（*llm.Client 或 *llm.Resilient）。非 nil 时优先于 Model。
	LLM        llm.Generator
	Dispatcher *dispatch.Dispatcher
	Merger     *dispatch.Merger
	ToolPool   toolpool.Pool
	ToolSpecs  []toolpool.ToolSpec
	Knowledge  rag.KnowledgeService
	// WC 工作副本池（OCR delegate 前置使用；nil = 委托前置不生效）。
	WC        *workcopy.Pool
	Bus       *oagentprogress.Bus[progress.Event] // 任务进度事件（v3.1 SSE；nil 安全）
	EventSeen *progress.Dedup                     // 实例级事件去重环（v6.12 P1：本地发布登记 + fanout 回灌查环；nil = 旧语义）
	Notify    *notify.Notifier                    // 终态 IM 推送（v3.1 octopus；nil 安全）
	Sink      *sink.Reporter                      // 审查结果上报（ARGUS_SINK_URL 非空启用；nil 安全，失败只告警）
	Poster    *poster.Poster
	Log       logx.Logger
	Version   string
	// Audit 审计日志（v3.86.0：verdict.posted / fix.push 等关键动作留痕；
	// oagentkit/obs/audit nil 接收者安全，未注入 = 只少审计不报错）。
	Audit *audit.Logger

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
	task, err := r.Store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("runner: 任务 %s 不存在: %w", taskID, err)
	}
	// 按仓旋钮单次加载（v3.187.0）：repo_settings 覆盖 → 全局默认，预算闸/
	// 任务预算/diff 上限三处消费全程共用（runner_knobs.go）。
	knobs := r.loadTaskKnobs(ctx, task)
	taskCost := llm.NewCostTracker()
	// 任务级 MCP 调用元数据：随 ctx 流入该任务全部工具调用的 _meta，server 侧
	// 可关联调用来源（哪次评审在调我）；Q&A 等非任务路径无注入。
	ctx = toolpool.WithCallMeta(ctx, map[string]any{
		"task_id": taskID,
		"repo":    task.Owner + "/" + task.Repo,
		"mr":      task.Number,
	})
	// Budget 闸后创建（Phase1①）：可能仍为 nil（panic 发生在闸前），失败收尾
	// 路径必须 nil 守卫。
	var budget *llm.Budget
	// v3.185.4：任务体统一 panic 出口——上游 queue/worker 的 panic 隔离只记
	// 日志不留终态（心跳过期复位重跑），对确定性 panic 是「复位→再炸」死循环
	// （live 实测：影响面注入 nil map panic 后任务卡 running）；argus 的
	// failed-retry 退避是重试正道，panic 必须经 handleRunFailure 落 failed 终态。
	if err = guardPanic(r.Log, task.ID, func() error {
		start := time.Now()
		// 审查开始留痕：在 MR 上发一条"审查中"评论，让用户知道 Argus 已接管。
		r.postStartMarker(ctx, task)

		// Phase1①：先闸后建——intercepted 时 Budget 尚未创建（也不需要）。
		intercepted, granted := r.budgetGate(ctx, task, knobs, r.Cfg.RealTokenBudget)
		if intercepted {
			return nil
		}
		budget = dispatch.NewTokenBudget(int(granted))
		ctx = r.execContext(ctx, task, budget, taskCost)
		wf := r.wireWorkflow(ctx, task, budget)
		r.publish(progress.EventTaskRunning, task, "", "")

		if rerr := r.run(ctx, task, wf, budget, start, taskCost, knobs); rerr != nil {
			r.handleRunFailure(ctx, task, budget, rerr)
		}
		return nil
	}); err != nil {
		// 守卫唯一上抛 = 任务体 panic：此时 run 已中断，handleRunFailure 未及
		// 执行——在守卫外补一次失败终态（其自身若再炸仅日志，终态缺失由心跳
		// 复位兜底，但确定性 panic 已从「无限复位重炸」变为 failed-retry 退避）
		defer func() {
			if rec := recover(); rec != nil {
				r.Log.Error("argus.runner.finalize_panic", "task", task.ID,
					"panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			}
		}()
		r.handleRunFailure(ctx, task, budget, err)
		return err
	}
	return nil
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

	// CI 失败归因（v3.71.0）：不依赖 MR 语义（push 的 commit 无 CR 可拉），
	// 在 prepareContext 之前分派——输入是失败 job 日志而非 diff。
	if task.Kind == domain.TaskKindCIAnalysis {
		return r.runCIAnalysis(ctx, task, start)
	}

	// 本地 diff 预审（v3.123.0）：同 ci_analysis 不依赖 MR 语义，prepareContext
	// 之前分派——输入是提交方送来的 diff 文本（artifacts），无平台拉取。
	if task.Kind == domain.TaskKindDiffPreview {
		return r.runDiffPreview(ctx, task, start)
	}

	pr, rc, diff, stale, err := r.prepareContext(ctx, task, knobs)
	if err != nil {
		return err
	}
	if stale {
		return nil // stale 终态已落地
	}

	// 写作面分支（v3.71.0）：describe/changelog 不进 R 系列审查管线与插件派发，
	// 单步 LLM 写作 → 平台写回，独立收口。
	if task.Kind == domain.TaskKindDescribe || task.Kind == domain.TaskKindChangelog {
		return r.runWriting(ctx, task, pr, diff, start)
	}

	ptask := r.buildPluginTask(ctx, task, pr, rc, diff, wf)
	r.applyTaskReportTemplate(&ptask, task, rc, knobs)

	// Phase 2 Evidence v2：证据核验读取面注入（任务期②字段；wf 跨任务复用，
	// Run 返回必清防 stale 泄漏）。pr 非 nil（CI/diff 分支已早退）且有池才注入；
	// 构造失败（无凭证等）保持 nil=集合级降级，不阻断审查。
	if pr != nil && r.WC != nil {
		wf.SourceReader = &workcopySourceReader{ctx: ctx, wc: r.WC, key: worktreeKey(pr)}
		defer func() { wf.SourceReader = nil }()
	}

	var out *plugin.WorkflowOutput
	switch task.Kind {
	case domain.TaskKindRequirementCompliance:
		out, err = wf.Run(ctx, ptask, budget)
	default: // code_review / visual_check：R4+R5 only
		out, err = wf.RunReviewOnly(ctx, ptask, budget)
	}
	if err != nil {
		return err
	}

	r.recordRegret(ctx, out, pr)

	// 组装报告（规模统计/CI 状态/影响面段/行内上限，详见 helper 注释）。
	rep := r.assembleReport(task, pr, ptask, out, taskCost, rc)

	// 修复验证环（v3.74.0）：配置了隔离包装命令且本次审查有工作副本可用时，
	// 对带 suggested_fix 的意见做沙箱应用+编译验证，通过者渲染「✅ 沙箱验证
	// 通过」。任何失败都静默降级为不标注——验证环绝不阻断审查主链路。
	r.verifyFixes(ctx, task, pr, out)

	// 回帖（staleness 门控在 poster 内）。
	res, err := r.Poster.Post(ctx, task, *pr, diff, out.Verdict, rep)
	// 活检：回帖层是否被真正调用（成功/失败/stale 都算执行过）
	plugin.ReportLayerExec(plugin.LayerPoster)
	if err != nil {
		// 回帖最终失败 → task=failed + {plugin=poster} 计数（§10.2）
		plugin.ReportPluginFailure("poster")
		return fmt.Errorf("回帖失败: %w", err)
	}

	return r.finalizeAndAnnounce(ctx, task, pr, out, res, rep, diff, start)
}

// applyTaskReportTemplate v6.11.0 报告模板四级裁决：任务级（commentcmd
// --template / API 注入）优先——须 IsValid 复核（API 建任务可绕开 commentcmd
// 词表校验，非法值告警弃用）；任务级缺席时落 repo_settings admin > 仓 yaml >
// env 链。R4 builtin 适配器与 workflow 综合段都经此 Artifacts 键消费。
func (r *Runner) applyTaskReportTemplate(ptask *plugin.Task, task *storage.Task,
	rc *plugin.RepoConfig, knobs taskKnobs) {
	if v := ptask.Artifacts[plugin.ArtifactReportTemplate]; !reporttmpl.IsValid(v) {
		if v != "" {
			r.Log.Warn("argus.runner.task_template_invalid", "task", task.ID, "value", v)
		}
		ptask.Artifacts[plugin.ArtifactReportTemplate] = r.resolveReportTemplateChain(rc, knobs)
	}
}

// assembleReport 报告输入组装的 run 侧收口：buildReport 骨架 + 结论行/规则 +
// 规模统计 + CI 状态 + 影响面折叠段 + 行内评论上限（模板渲染交给 reportview）。
func (r *Runner) assembleReport(task *storage.Task, pr *plugin.PRContext, ptask plugin.Task,
	out *plugin.WorkflowOutput, taskCost *llm.CostTracker, rc *plugin.RepoConfig) reportview.Report {
	rep := r.buildReport(task, pr, out, taskCost)
	rep.VerdictText = reportview.VerdictText(out.Rule, countMissingPartial(out.Report))
	rep.Rule = out.Rule
	// v6.11.0 规模统计（analyst 报告头）+ CI 状态（meta 行）：数据就地汇总，
	// 零额外调用（CIStatus 已在 buildPRContext 经 capability 门取好）。
	if ptask.Diff != nil {
		rep.ScaleFiles = len(ptask.Diff.Files)
		for _, f := range ptask.Diff.Files {
			rep.ScaleAdditions += f.Additions
			rep.ScaleDeletions += f.Deletions
		}
	}
	rep.CIStatus = pr.CIStatus
	// 影响面段（v3.183.0）：injectImpactView 产出的快照渲染为评论折叠段
	//（快照已在 ptask.Artifacts，零额外计算；坏 JSON 静默省段——不阻断回帖）。
	if raw := ptask.Artifacts[plugin.ArtifactImpactView]; raw != "" {
		var snap impactview.Snapshot
		if err := json.Unmarshal([]byte(raw), &snap); err == nil {
			rep.ImpactSection = impactview.RenderReportSection(&snap)
			// 交互图路标（v5.6.0 附链接；v5.6.1 任务绑定 share 令牌——MR 评审人
			// 免 API 令牌直开图谱页，secret 空=鉴权关闭时不带参数）。
			if rep.ImpactSection != "" && r.Cfg.PublicURL != "" && len(snap.Nodes) > 0 {
				link := fmt.Sprintf("%s/tasks/%s/impact", r.Cfg.PublicURL, task.ID)
				if tok := impactview.ImpactShareToken(r.Cfg.APIToken, task.ID); tok != "" {
					link += "?share=" + tok
				}
				rep.ImpactSection += fmt.Sprintf(
					"\n> 🔎 [交互式影响面图](%s)（自包含单页：缩放/拖拽/搜索/类型过滤/邻域聚焦/导出）\n", link)
			}
		}
	}
	// 追溯链段（批 F）：finish 已把 TraceChain 挂 ptask.Artifacts——此处预渲染
	//（纯报告增强零裁决影响；坏 JSON/缺位静默省段不阻断回帖）。
	if tc := plugin.TraceChainOf(ptask.Artifacts); tc != nil && len(tc.Traces) > 0 {
		var tb strings.Builder
		tb.WriteString("\n<details><summary>需求追溯链（issue→AC→代码→测试）</summary>\n\n")
		if tc.SourceRef != "" {
			tb.WriteString("- 来源锚: " + tc.SourceRef + "\n")
		}
		tb.WriteString("\n| AC | 代码 | 测试 | 断链 |\n|---|---|---|---|\n")
		for _, tr := range tc.Traces {
			tests := tr.TestStatus
			if len(tr.Tests) > 0 {
				tests = strings.Join(tr.Tests, ",") + "（" + tr.TestStatus + "）"
			}
			tb.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
				tr.ACID, strings.Join(tr.CodeFiles, ","), tests, tr.CodeStatus))
		}
		if len(tc.Broken) > 0 {
			tb.WriteString("\n断链: " + strings.Join(tc.Broken, "、") + "\n")
		}
		tb.WriteString("\n</details>\n")
		rep.TraceSection = tb.String()
	}
	// 仓库级行内评论上限（v3.15.0 review.max_comments；0 = 不限，钳制在 poster）
	rep.MaxInlineComments = rc.Review.MaxComments
	return rep
}

// prepareContext PR 上下文与前置判定：平台 API 组装 → 仓库主权配置单次加载 →
