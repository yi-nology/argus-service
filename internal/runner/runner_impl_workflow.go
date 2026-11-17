// runner_impl_workflow.go —— Runner 工作流装配与任务回调链的包级实现。

package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/obsx"
	"github.com/oagentkit/llm/port"
	"github.com/oagentkit/skillkit/skill"
	"github.com/yi-nology/argus/internal/langfuseobs"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/reporttmpl"
	"github.com/yi-nology/argus/internal/reposettings"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/workflow/requirement"
)

// skillText 的包级实现（转发壳见原文件；方法签名中的 ctx 为占位，不入实现）。
func skillText(r *Runner) string {
	if r.SkillProvider == nil || len(r.SkillRefs) == 0 {
		return ""
	}
	r.skillMu.Lock()
	if r.skillLoaded && time.Now().Before(r.skillRefresh) {
		cached := r.skillCache
		r.skillMu.Unlock()
		return cached
	}
	r.skillMu.Unlock()

	// 用 Background ctx：skill 是本地文件读取，不需要任务级取消。
	bgCtx := context.Background()
	text := r.loadSkillText(bgCtx)

	r.skillMu.Lock()
	r.skillCache = text
	r.skillLoaded = true
	r.skillRefresh = time.Now().Add(10 * time.Minute)
	r.skillMu.Unlock()
	return text
}

// loadSkillText 的包级实现（转发壳见原文件）。
func loadSkillText(r *Runner, bgCtx context.Context) string {
	var parts []string
	toolMode := r.Cfg.SkillMode == "tool"
	if toolMode {
		// 渐进披露：清单用 agentkit skill.ListPrompt 渲染——加粗主词是规范引用名
		//（use_skill 加载依据），frontmatter 展示名降为括注；模型用展示名回填时
		// AsSkillTool 会别名归一化（v0.7.0 起闭环）
		lister, ok := r.SkillProvider.(skill.Lister)
		if !ok {
			r.Log.Warn("argus.runner.skill_list_failed", "error", "provider 不支持 ListSkills")
			return ""
		}
		metas, err := lister.ListSkills(bgCtx)
		if err != nil {
			r.Log.Warn("argus.runner.skill_list_failed", "error", err.Error())
			return ""
		}
		allowed := map[string]bool{}
		for _, ref := range r.SkillRefs {
			if i := strings.Index(ref, "@"); i >= 0 {
				ref = ref[:i]
			}
			allowed[ref] = true
		}
		var filtered []skill.Meta
		for _, m := range metas {
			if len(allowed) == 0 || allowed[m.Name] {
				filtered = append(filtered, m)
			}
		}
		// 两级预算降级（v0.10.18，对标 ZCode skills 注入预算）：清单超 20K 字节
		// 先降纯名单、仍超再截断注明；预算内渲染与 ListPrompt 完全一致。
		return skill.RenderList(filtered, skill.DefaultListBudget)
	}
	for _, ref := range r.SkillRefs {
		name, ver := ref, ""
		if i := strings.Index(ref, "@"); i >= 0 {
			name, ver = ref[:i], ref[i+1:]
		}
		s, err := r.SkillProvider.Resolve(bgCtx, skill.Ref{Name: name, Version: ver, Source: "file"})
		if err != nil {
			r.Log.Warn("argus.runner.skill_resolve_failed", "ref", ref, "error", err.Error())
			continue
		}
		parts = append(parts, fmt.Sprintf("--- skill:%s (checksum=%s) ---\n%s", s.Name, s.Checksum, s.Content))
	}
	return strings.Join(parts, "\n")
}

// WorkflowFor 的包级实现（转发壳见原文件）。
func workflowFor(r *Runner, budget *llm.Budget) *requirement.Workflow {
	return r.buildWorkflow(budget)
}

// WorkflowFactory 的包级实现（转发壳见原文件）。
func workflowFactory(r *Runner) plugin.WorkflowFactory {
	return func(budget *llm.Budget) plugin.WorkflowRunner {
		return r.buildWorkflow(budget)
	}
}

// buildWorkflow 的包级实现（转发壳见原文件）。
func buildWorkflow(r *Runner, budget *llm.Budget) *requirement.Workflow {
	// 决策使用模式：use_skill 工具进 R3 工具表（与静态注入互斥，ARGUS_SKILL_MODE）
	var skillTool tool.BaseTool
	if r.Cfg.SkillMode == "tool" && r.SkillProvider != nil && len(r.SkillRefs) > 0 {
		// 显式内容上限（v3.39.0）：与清单 RenderList 预算构成两级纪律
		if st, e := skill.AsSkillTool(r.SkillProvider, r.SkillRefs,
			skill.WithContentCap(r.Cfg.SkillContentCap)); e == nil {
			skillTool = st
		} else {
			r.Log.Warn("argus.runner.skill_tool_failed", "error", e.Error(), "fallback", "静态注入")
		}
	}
	// 优先使用装配期构造的 Generator（*llm.Client 或 *llm.Resilient 降级链）；
	// 通过 BudgetInjector 注入任务级预算（Resilient 副本共享熔断状态，Client 浅拷贝）。
	var gen llm.Generator
	if r.LLM != nil {
		gen = r.LLM
		if budget != nil {
			if bi, ok := gen.(llm.BudgetInjector); ok {
				gen = bi.WithBudget(budget)
			}
		}
		// P6 阶段路由：R1/R2 覆盖链各自注入任务预算后注册（精确/前缀匹配，
		// "R1" 命中 "R1a"）；未覆盖阶段走缺省链
		if len(r.StageLLMs) > 0 {
			sr := llm.NewStageRouter(gen)
			for stage, sg := range r.StageLLMs {
				if bi, ok := sg.(llm.BudgetInjector); ok && budget != nil {
					sg = bi.WithBudget(budget)
				}
				sr.Use(stage, sg)
			}
			gen = sr
		}
	} else {
		c := llm.NewClient(r.Model, r.ModelName, budget)
		// 窗口与输出预留注入 → Generate 发送前输入自守恒（fitInput）
		c.ContextTokens = r.Cfg.LLM.ContextToken
		c.MaxOutputTokens = r.Cfg.MaxTokensResolved()
		// v5.5.2：同 bootstrap——单模型 Client 也吃韧性参数（MaxRetries 含首次，+1 对齐）。
		c.MaxRetries = r.Cfg.LLM.LLMResilienceRetries + 1
		c.BaseDelay = r.Cfg.LLM.LLMResilienceBaseDelay
		c.Limiter = r.LLMLimiter
		// agentkit llm 用 OnUsage 回调替代直接调用 plugin.ReportLLMTokens
		c.OnUsage = func(stage string, prompt, completion int) {
			plugin.ReportLLMTokens(stage, r.ModelName, prompt+completion)
		}
		gen = c
	}
	// R3 ReAct 模型解析：阶段覆盖 > RawModel；配置了备模型则包 FailoverModel
	//（ADK 直调 BaseChatModel，不走 Resilient 链）。⚠️ 这层包**不可删**：阶段覆盖的
	// R3Model 是裸模型（buildStageModel 出 openai.NewChatModel，无降级），靠它补
	// 主备切换；仅当来源是 RawModel（v0.10.9+ 自带 failover）时它才是冗余。
	r3Chat := r.R3Model
	if r3Chat == nil && r.LLM != nil {
		r3Chat = r.LLM.RawModel()
	}
	if r3Chat != nil && r.FallbackModel != nil {
		// v0.10.11：链节结构化（ChainLink{Model,Name}），消平行切片错位
		fm := llm.NewFailoverModel(
			llm.ChainLink{Model: r3Chat, Name: r.ModelName},
			llm.ChainLink{Model: r.FallbackModel, Name: r.FallbackName},
		)
		if r.Log != nil {
			fm.OnFailover = func(from, to, reason string) {
				r.Log.Warn("argus.llm.failover", "from", from, "to", to,
					"stage", "R3(react)", "reason", reason)
			}
		}
		r3Chat = fm
	}
	return &requirement.Workflow{
		LLM:                gen,
		R3ChatModel:        r3Chat,
		SkillTool:          skillTool,
		SDK:                r.SDK,
		Dispatcher:         r.Dispatcher,
		Merger:             r.Merger,
		Tools:              r.ToolPool,
		ToolSpecs:          r.ToolSpecs,
		Knowledge:          r.Knowledge,
		SkillText:          r.skillText,
		Log:                r.Log,
		ReqBodyMaxChars:    r.Cfg.ReqBodyMaxChars(),
		ReqIssueMaxChars:   r.Cfg.ReqIssueMaxChars(),
		GetFileMaxChars:    r.Cfg.GetFileMaxRunes(),
		GetIssueMaxChars:   r.Cfg.GetIssueMaxRunes(),
		GetDiffMaxChars:    r.Cfg.GetDiffMaxRunes(),
		ReflectRounds:      r.ReflectRounds,
		ReflectModel:       r.ReflectModel,
		ReflectEvidence:    r.ReflectEvidence,
		CompletenessPolicy: r.CompletenessPolicy,
		CompletenessRetry:  r.CompletenessRetry,
	}
}

// wireWorkflow 的包级实现（转发壳见原文件）。
func wireWorkflow(r *Runner, ctx context.Context, task *storage.Task, budget *llm.Budget) *requirement.Workflow {
	wf := r.buildWorkflow(budget)
	// 本仓评审记忆索引（v3.35.0）：dream 周期导出，此处装载注入 R3。
	// v5.0.0 文件面退役：内核是唯一路径；未装配（ARGUS_MEMORY_KERNEL 关）=
	// 空串（R3 提示词无该段），功能未启用零开销。
	// 内核召回走八段 + MemoryBand 防伪（sessionID=任务 ID 防回声）。
	if r.MemKernel != nil {
		ref := memorykernel.Ref{Platform: task.Platform, Owner: task.Owner, Repo: task.Repo}
		wf.MemoryIndex = r.MemKernel.RecallIndex(ctx, memorykernel.NamespaceOf(ref),
			memorykernel.OrgNamespaceOf(tenant.FromContext(ctx)), task.ID, "", r.Cfg.MemoryIndexBudget)
	}
	// 外部工具调用上限可配（v3.41.0）
	wf.ExternalToolCallLimit = r.Cfg.ExternalToolCallLimit
	// 仓库级评审规范（v3.38.0）：.argus/REVIEW.md @ head，R3/R4 注入
	wf.RepoGuide = r.RepoGuideFor(ctx, task.Platform, task.Owner, task.Repo, task.HeadSHA)
	// 阶段进度事件（v3.40.0）：R1/R2/R3/R35 进入时发 task.stage，SSE 消费者可展示。
	// v6.1.5 执行期进度可见化：同一回调里把 start marker 原地更新为带阶段进度条的
	// 「审查中」文案——onStage 每阶段回调恰好一次且 setStartMarker 扫锚点原地更新，
	// 天然节流；完成/失败三态收口（runner_lifecycle.go / writing.go）随后整体覆盖
	// marker，进度版只服务执行期。ctx 直接沿用 wireWorkflow 的任务级 ctx：onStage
	// 由 workflow 在 wf.Run(ctx,…) 的同一请求生命周期内同步回调（requirement.onStage），
	// ctx 必然存活，无须 context.Background()——任务取消时更新随之中止（更新失败
	// 仅 Warn，不阻断审查主链路，纪律由 setStartMarker 既有实现保证）。
	wf.OnStage = func(stage string) {
		r.publishStage(task, stage)
		r.setStartMarker(ctx, task, startMarkerProgressBody(task.ID, stage))
	}
	// 详情存档目录（v5.0.0 spill 收编）：超限工具全文落盘任务目录，get_detail 跨
	// checkpoint 复活/重启取回不失效；顺带 best-effort 清理 7 天前过期任务目录。
	wf.SpillDir = filepath.Join(r.Cfg.WorkRoot, "spill", task.ID)
	return wf
}

// execContext 的包级实现（转发壳见原文件）。
func execContext(r *Runner, ctx context.Context, task *storage.Task, budget *llm.Budget, taskCost *llm.CostTracker) context.Context {
	// 终审 Critical：执行实例 nonce——stale 重跑（ResetRunningToPending 不 bump
	// attempt）同 task+attempt 计数器重启，稳定键无 nonce 会跨执行撞唯一索引
	// 静默少计。必须在闭包（recordUsageEvent 调用点）创建前注入：闭包捕获本地
	// ctx 变量，此处先行赋值即保证闭包可见 nonce。缺席才铸新（上游/测试显式
	// 注入的确定性 nonce 不覆盖）。
	if execNonceFrom(ctx) == "" {
		ctx = withExecNonce(ctx, newExecNonce())
	}
	// Phase1③：任务作用域 LLM 调用计数器——rec.Iteration 随 UsageRecord 进入
	// recordUsageEvent，铸造稳定 invocation_id（同物理调用重复投递撞唯一索引
	// 收敛）。注入必须在 InitCallbacks 之前，使 eino 回调链 ctx 可见。
	ctx = llm.WithCallCounter(ctx)
	obsOpt := obsx.Options{SlowThreshold: 60 * time.Second}
	handlers := []callbacks.Handler{obsx.NewTracingHandler(r.Log, obsOpt)}
	// 用量 handler 装配条件含 Store（#32）：逐调用实销埋点是计费账本、不随
	// 成本指标开关（r.Cost）缺席而失效；Cost 为空时仅跳过进程级成本行。
	if r.Cost != nil || r.Store != nil {
		handlers = append(handlers, llm.NewUsageHandler(func(rec llm.UsageRecord) {
			// v3.94.0：agentkit v0.12 起校准内化为 FitModel 实例槽（主路径自动
			// 校准），原 wrap.RecordUsage 回灌调用点删除。旁路无 stage 语义
			//（RunInfo 仅组件类型），stage 记空、模型按组件类型归账。
			budget.Add(rec.PromptTokens + rec.CompletionTokens)
			// #32 逐调用实销埋点：与预算回灌同点落账（best-effort，写失败只告警，
			// 终态差额兜底补账）——运行中/失败/重试的每次调用都进 usage_event。
			r.recordUsageEvent(ctx, task, rec)
			// Langfuse GenAI 语义 span（OTel 管线 → /api/public/otel）：未配
			// OTel endpoint 时全局 tracer 为 no-op，零开销直通。
			langfuseobs.Record(ctx, langfuseobs.TaskAttr{
				TaskID: task.ID, Platform: task.Platform, Owner: task.Owner,
				Repo: task.Repo, Number: task.Number, TaskKind: task.Kind,
				Stage: rec.Labels["stage"],
			}, rec)
			// v3.150.0：Record → RecordPriced（旧 Record 按 1K 计费已退役，
			// 新接口单位写在类型上=每 1M token；argus 配置侧仍是 per-1K，×1000 换算）。
			rate := r.costRate(rec.Model)
			// v3.180.0 省 token 盲区修复：CachedTokens 是命中前缀缓存的 prompt token
			// （便宜得多），旧记账按全价入账 → 成本高估。摊薄 InputPerM 折扣缓存命中
			// 部分（cacheAdjustedInput），token 计数保持真实。llm v0.17.0 起用
			// RecordPricedCached 让 cached 落进 CostRecord.CachedTokens（CacheHitRatio
			// 可派生）——缓存折扣本就该调用方在 Price 层表达（上游 Price 文档明示），
			// 故 cacheAdjustedInput 保留而非上游内置。
			inputPerM := cacheAdjustedInput(rate[0]*1000, rec.PromptTokens, rec.CachedTokens, r.Cfg.CacheReadRatio())
			price := port.Price{InputPerM: inputPerM, OutputPerM: rate[1] * 1000}
			taskCost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			if r.Cost != nil {
				r.Cost.RecordPricedCached(rec.Model, "", rec.PromptTokens, rec.CachedTokens, rec.CompletionTokens, price)
			}
			// 缓存命中率观测（前缀缓存命中率=cached/prompt）：argus 侧 Prometheus 时序
			// 监控面（Grafana），与上游 CostTracker 的 pull 式 Summary 互补不冗余。
			plugin.ReportLLMCache(rec.Model, rec.CachedTokens, rec.PromptTokens)
		}))
	}
	return callbacks.InitCallbacks(ctx, nil, handlers...)
}

// loadTaskKnobs 的包级实现（转发壳见原文件）。
func loadTaskKnobs(r *Runner, ctx context.Context, task *storage.Task) taskKnobs {
	k := taskKnobs{
		monthlyBudget:   r.Cfg.RepoMonthlyTokenBudget,
		taskTokenBudget: r.Cfg.EffectiveTaskTokenBudget(),
		maxDiffChars:    r.Cfg.MaxDiffCharsResolved(),
	}
	row, err := r.Store.GetRepoSetting(ctx, task.Platform, task.Owner, task.Repo)
	if err != nil {
		r.Log.Warn("argus.runner.knobs_load_failed", "task", task.ID, "error", err.Error())
		return k
	}
	if row == nil {
		return k
	}
	rs, err := reposettings.Parse([]byte(row.SettingsJSON))
	if err != nil {
		r.Log.Warn("argus.runner.knobs_corrupt", "task", task.ID, "error", err.Error())
		return k
	}
	k.monthlyBudget = rs.MonthlyTokenBudgetOr(k.monthlyBudget)
	k.taskTokenBudget = rs.TaskTokenBudgetOr(k.taskTokenBudget)
	k.maxDiffChars = rs.MaxDiffCharsOr(k.maxDiffChars)
	// v6.11.0 admin 层模板覆盖：词表复核（reposettings 刻意零依赖不校验词表，
	// 形状外的合法性归消费端）——非法值告警回退空（= 未设置，落下游链）。
	k.reportTemplate = rs.ReportTemplateOr("")
	if k.reportTemplate != "" && !reporttmpl.IsValid(k.reportTemplate) {
		r.Log.Warn("argus.runner.knobs_template_invalid", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "value", k.reportTemplate)
		k.reportTemplate = ""
	}
	r.Log.Info("argus.runner.knobs_repo_override",
		"task", task.ID, "repo", task.Owner+"/"+task.Repo,
		"monthly_budget", k.monthlyBudget, "task_budget", k.taskTokenBudget,
		"max_diff_chars", k.maxDiffChars, "report_template", k.reportTemplate)
	return k
}
