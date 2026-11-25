package adapter_builtin

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"

	"github.com/oagentkit/goagent-runtime/agentloop"
	"github.com/oagentkit/goagent-runtime/pipeline"

	"github.com/yi-nology/argus/internal/agentstream"
	"github.com/yi-nology/argus/internal/loopadapt"
	"github.com/yi-nology/argus/internal/plugin"
)

// ensureSkillText 惰性解析 skillRef（含 checksum 头，防内容漂移难排查 §9.1），10 分钟刷新。
// 刷新 IO 在锁外进行（skillMu 只护缓存字段——Resolve 是文件读，握锁会阻塞并发
// Invoke 的读者）；ctx 取值保留、取消/截止剥离——后台刷新不随单次请求死亡，
// 但以 10s 封顶防悬挂。并发双刷新无碍：同输入同输出，后写者胜。
func (r *Reviewer) ensureSkillText(ctx context.Context) {
	if r.skills == nil || len(r.skillRefs) == 0 {
		return
	}
	r.skillMu.Lock()
	fresh := r.skillLoaded && time.Now().Before(r.skillRefresh)
	r.skillMu.Unlock()
	if fresh {
		return
	}
	ioCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var parts []string
	for _, ref := range r.skillRefs {
		s, err := r.skills.Resolve(ioCtx, ref)
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("--- skill:%s (checksum=%s) ---\n%s", s.Name, s.Checksum, s.Content))
	}
	text := strings.Join(parts, "\n")
	r.skillMu.Lock()
	r.skillText = text
	r.skillLoaded = true
	r.skillRefresh = time.Now().Add(10 * time.Minute)
	r.skillMu.Unlock()
}

// Invoke 执行审查：toolsHook 非 nil 走 ReAct（模型可经 get_file 等工具核实上下文），
// 失败自动落回单轮 GenerateJSON；否则直接单轮。
func (r *Reviewer) Invoke(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	if r.toolsHook != nil {
		res, err := r.invokeReAct(ctx, task)
		if err == nil {
			return res, nil
		}
		if r.log != nil {
			r.log.Warn("argus.builtin.react_fallback", "plugin", r.manifest.Name,
				"error", err.Error(), "fallback", "单轮调用")
		}
		// 降级透出（v3.8.0）：ReAct 失败静默落回单轮曾让同一 "by xxx" 标签掩盖
		// 两种审查强度；Merger 把含标记的 Notes 行透出为报告 Degraded。
		res, serr := r.invokeSingle(ctx, task)
		if serr == nil {
			res.Notes = strings.TrimSpace(res.Notes + "\n" + plugin.DegradationMarker +
				" ReAct 执行失败，已降级为单轮审查（无工具核实，覆盖深度受限）")
		}
		return res, serr
	}
	return r.invokeSingle(ctx, task)
}

// invokeReAct ReAct 模式：工具表（toolprior 三层约束）+ goagent-runtime
// agentloop 运行样板（agentrun 迁移，② 第二消费方）。
// 注意：RawModel 旁路绕过 llm 预算注入——token 成本经 UsageHandler 记账并回灌
// 任务预算（v3.36.0，旁路消耗会计入 Remaining）；预算耗尽可能在下一步 Generate
// 侧短路，ReAct 循环本身不中途打断。
func (r *Reviewer) invokeReAct(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	tools, strategy, err := r.toolsHook(ctx, task, task.Budget)
	if err != nil {
		return plugin.Result{}, fmt.Errorf("工具表构造失败: %w", err)
	}
	if len(tools) == 0 {
		return plugin.Result{}, fmt.Errorf("工具表为空")
	}

	r.ensureSkillText(ctx)
	sys := r.buildReviewSystem(task, strategy)
	user := r.buildReviewUser(ctx, task) +
		"\n请审查以上变更：先用工具核实必要的上下文，再输出 findings JSON（无问题输出空数组）。只输出合法 JSON。"

	maxIter := r.reactMaxIterations
	if maxIter <= 0 {
		maxIter = 12 // 兼容未注入（直构/旧装配）：保持历史默认
	}

	// 流式（task.DeltaSink 非 nil 才挂——nil 不挂 Emitter，模型走 Generate，
	// 旧 DisableStreaming 回滚开关语义）；工具轨迹与流式解耦挂 pipeline
	// Handler（TraceTool，DeltaSink nil 照走——单源 ToolTraceLogger）。
	var streamer *agentstream.Streamer
	var emitter agentloop.Emitter
	if task.DeltaSink != nil {
		streamer = agentstream.New(task.DeltaSink)
		emitter = loopadapt.NewStreamEmitter(streamer)
		defer streamer.Close() // 收口冲刷残尾 + 终稿快照后仅剩 reasoning 尾
	}
	logTrace := plugin.ToolTraceLogger(r.log, "argus.builtin", "plugin", r.manifest.Name)
	trace := func(name string, bytes int, d time.Duration, err error) {
		if logTrace != nil {
			logTrace(name, bytes)
		}
		plugin.ReportToolCall(ctx, "builtin", name, d, err)
	}

	// 每 attempt 全新循环：内存快照只保本次 Run 内中断自洽（跨重启审查状态
	// 归任务存储）；失败回喂重试一次（整程重来，工具表按 attempt 重建——
	// WithCallLimit 限流计数按尝试重置的原语义），仍败交 invokeSingle 降级。
	// 装配/重试/未知态处置单源在 loopadapt.RunReAct（三条 ReAct 线共用）。
	final, err := loopadapt.RunReAct(ctx, loopadapt.ReActSpec{
		SessionPrefix: "builtin",
		Model:         func() loopadapt.ChatModel { return loopadapt.NewEinoChat(r.reviewerReactModel()) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			t, _, terr := r.toolsHook(ctx, task, task.Budget)
			return t, terr
		},
		System:    sys,
		MaxRounds: maxIter,
		Emitter:   emitter,
		Trace:     func(pt pipeline.Tool) pipeline.Tool { return loopadapt.TraceTool(pt, trace) },
		RetryHint: plugin.ReactRetryHint,
		Label:     "ReAct",
		OnReset: func() {
			if streamer != nil {
				streamer.OnReset()
			}
		},
		OnFinal: func(f string) {
			// 终稿权威快照（agentloop 不产 text 快照事件）：吸收本轮缓冲后整段下发。
			if streamer != nil {
				streamer.OnSnapshot(agentstream.PhaseText, f)
			}
		},
		OnStats: func(s loopadapt.ReActStats) {
			plugin.ReportAgentLoop(ctx, "builtin", s.Status, s.Attempts, s.Rounds, s.ToolCalls, s.Uncertain)
		},
	}, user)
	if err != nil {
		return plugin.Result{}, err
	}

	out, e := plugin.DecodeFindingsJSON(final)
	if e != nil {
		return plugin.Result{}, fmt.Errorf("ReAct 输出解析失败: %w", e)
	}
	for i := range out.Findings {
		out.Findings[i].By = r.manifest.Name
	}
	return plugin.Result{Findings: out.Findings, Notes: out.Notes}, nil
}

// invokeSingle 单轮调用（原路径；ReAct 的降级兜底）。
func (r *Reviewer) invokeSingle(ctx context.Context, task plugin.Task) (plugin.Result, error) {
	r.ensureSkillText(ctx)

	sys := r.buildReviewSystem(task, "")
	user := r.buildReviewUser(ctx, task) +
		"\n请审查以上变更并输出 findings JSON（无问题输出空数组）。"

	msgs := []*schema.Message{
		schema.SystemMessage(sys),
		schema.UserMessage(user),
	}
	var out plugin.FindingsWire
	// 计入任务预算：构造期注入的 budget 为 nil（Reviewer 全局单例），任务级
	// 预算由 Dispatcher.Run 经 Task.Budget 传入——否则 R4 主成本对预算不可见。
	client := r.llm
	if task.Budget != nil {
		if bi, ok := r.llm.(llm.BudgetInjector); ok {
			client = bi.WithBudget(task.Budget)
		}
	}
	if err := client.GenerateJSON(ctx, "plugin:"+r.manifest.Name, msgs, &out); err != nil {
		return plugin.Result{}, err
	}
	for i := range out.Findings {
		out.Findings[i].By = r.manifest.Name
	}
	return plugin.Result{Findings: out.Findings, Notes: out.Notes}, nil
}
