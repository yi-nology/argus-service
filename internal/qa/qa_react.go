package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/llm"

	"github.com/yi-nology/goagent-runtime/agentloop"

	"github.com/yi-nology/argus/internal/agentstream"
	"github.com/yi-nology/argus/internal/loopadapt"
	"github.com/yi-nology/argus/internal/plugin"
)

// react ReAct 路径（historyText = 滚动摘要 + verbatim 轮次的注入文本；
// reportAnchor 非空时以最近审查报告为锚注入——ask_argus 追问路径 v3.141.0）。
//
// 执行底座 = goagent-runtime agentloop（② 整层引入首单，agentrun →
// agentloop 迁移）：回合预算 Budget.MaxRounds=8（原 MaxIterations）、
// System=instruction、工具走 pipeline（只读表 SafeReadOnly）、模型经
// loopadapt.NewEinoChat 适配；流式经 loopadapt.NewStreamEmitter →
// agentstream（DeltaSink nil = 不挂 Emitter，模型走 Generate——旧
// DisableStreaming 的回滚开关语义逐字保留）。
func (e *Engine) react(ctx context.Context, platform, owner, repo, number, question, historyText, reportAnchor string) (string, error) {
	// 预建一次：工具表构造失败提前暴露（重试轮按 attempt 重建，工具表不跨
	// attempt 复用——与旧 ToolsFactory 按尝试重建同款）。
	_, strategy, err := e.tools(ctx, platform, owner, repo, number)
	if err != nil {
		return "", err
	}
	// 本仓评审记忆索引（v3.39.0）：作答可参考既有结论；内容半可信，先转义提醒标签
	instruction := systemPrompt(ctx, strategy, historyText)
	if e.MemoryIndexFor != nil {
		if idx := e.MemoryIndexFor(ctx, platform, owner, repo); idx != "" {
			instruction += "\n\n===【本仓历史评审记忆（dream 导出索引，可参考既有结论）】===\n" + plugin.EscapeReminderTags(idx)
		}
	}
	if reportAnchor != "" {
		// 追问路径（v3.141.0）：报告为锚 + 工具可深挖（核实代码/调用方/历史账本），
		// 报告没有的信息允许用工具补充，但仍须 file:line 证据、不得编造。
		instruction += "\n\n" + plugin.DataFence(ctx, "最近审查报告（数据）", reportAnchor) +
			"\n用户的问题针对上述审查报告。优先以报告为锚作答；需要核实代码细节/调用方/历史处置时用工具，引用 file:line 证据。"
	}
	// 失败回喂重试一次（副作用守卫默认生效）；QA 产出是自然语言回答，
	// 重试提示不要求 JSON。v3.38.0 起提示经 ReminderNote 包装。
	qaRetryHint := "\n\n" + plugin.ReminderNote("retry-hint",
		"上一次执行因错误中断，请重新完整执行本任务并给出最终回答。")

	// 流式收敛器：DeltaSink 非 nil 才建（nil = 整段语义回滚开关）。defer
	// 收口在快照之后执行（LIFO：下面的 OnSnapshot 先于 Close 落地）。
	var streamer *agentstream.Streamer
	var emitter agentloop.Emitter
	if e.DeltaSink != nil {
		streamer = agentstream.New(func(d plugin.AgentDelta) {
			e.DeltaSink(ctx, platform, owner, repo, number, d)
		})
		emitter = loopadapt.NewStreamEmitter(streamer)
		defer streamer.Close()
	}

	// 每 attempt 全新循环：内存快照 store 只保本次 Run 内的中断自洽——跨
	// 重启的会话历史归 qa_sessions（historyText 注入），checkpoint 不承担
	// 跨进程恢复；重试 = 整程重来，不复用上一 attempt 的快照。装配/重试/
	// 未知态处置单源在 loopadapt.RunReAct（三条 ReAct 线共用）。
	answer, err := loopadapt.RunReAct(ctx, loopadapt.ReActSpec{
		SessionPrefix: "qa",
		Model:         func() loopadapt.ChatModel { return loopadapt.NewEinoChat(e.chatModel()) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			t, _, terr := e.tools(ctx, platform, owner, repo, number)
			return t, terr
		},
		System:    instruction,
		MaxRounds: 8,
		Emitter:   emitter,
		RetryHint: qaRetryHint,
		Label:     "react",
		OnReset: func() {
			if streamer != nil {
				streamer.OnReset()
			}
		},
		OnFinal: func(f string) {
			// 终稿权威快照：agentloop 不产 text 快照事件（终答只在 Result.Final），
			// 此处补喂——快照吸收本轮增量缓冲后整段下发（消费方以快照替换累积），
			// 随后 defer Close 冲刷 reasoning 残尾。
			if streamer != nil {
				streamer.OnSnapshot(agentstream.PhaseText, f)
			}
		},
	}, question)
	if err != nil {
		return "", err
	}
	return answer, nil
}

// single 单轮直调（ReAct 的降级兜底；带历史上下文与 PR 摘要）。
func (e *Engine) single(ctx context.Context, platform, owner, repo, number, question, historyText string) (string, error) {
	prCtx, err := e.loadContext(ctx, platform, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("加载 PR 上下文失败: %w", err)
	}
	client := llm.NewClient(e.chatModel(), "", nil)
	msgs := []*schema.Message{
		schema.SystemMessage(systemPrompt(ctx, "", historyText)),
		// v3.8.0：PR 标题/描述/作者（敌对输入）此前裸拼进 user message——
		// ReAct 失败时自动落回此路径，注入防线与故障率正相关倒挂。补围栏。
		schema.UserMessage(plugin.DataFence(ctx, "PR 上下文", prCtx) + "\n\n用户问题: " + question),
	}
	resp, err := client.Generate(ctx, "qa", msgs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
