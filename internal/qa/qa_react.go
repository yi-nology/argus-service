package qa

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/oagentkit/agent/agentrun"
	"github.com/oagentkit/llm"
	"github.com/yi-nology/argus/internal/agentstream"
	"github.com/yi-nology/argus/internal/plugin"
)

// react ReAct 路径（historyText = 滚动摘要 + verbatim 轮次的注入文本；
// reportAnchor 非空时以最近审查报告为锚注入——ask_argus 追问路径 v3.141.0）。
func (e *Engine) react(ctx context.Context, platform, owner, repo, number, question, historyText, reportAnchor string) (string, error) {
	// 预建一次：工具表构造失败提前暴露（运行时由 ToolsFactory 按尝试重建）
	_, strategy, err := e.tools(ctx, platform, owner, repo, number)
	if err != nil {
		return "", err
	}
	// 本仓评审记忆索引（v3.39.0）：作答可参考既有结论；内容半可信，先转义提醒标签
	instruction := systemPrompt(strategy, historyText)
	if e.MemoryIndexFor != nil {
		if idx := e.MemoryIndexFor(platform, owner, repo); idx != "" {
			instruction += "\n\n===【本仓历史评审记忆（dream 导出索引，可参考既有结论）】===\n" + plugin.EscapeReminderTags(idx)
		}
	}
	if reportAnchor != "" {
		// 追问路径（v3.141.0）：报告为锚 + 工具可深挖（核实代码/调用方/历史账本），
		// 报告没有的信息允许用工具补充，但仍须 file:line 证据、不得编造。
		instruction += "\n\n" + plugin.DataFence("最近审查报告（数据）", reportAnchor) +
			"\n用户的问题针对上述审查报告。优先以报告为锚作答；需要核实代码细节/调用方/历史处置时用工具，引用 file:line 证据。"
	}
	// 失败回喂重试一次（副作用守卫默认生效）；QA 产出是自然语言回答，
	// 重试提示不要求 JSON。v3.38.0 起提示经 ReminderNote 包装。
	qaRetryHint := "\n\n" + plugin.ReminderNote("retry-hint",
		"上一次执行因错误中断，请重新完整执行本任务并给出最终回答。")
	opts, closeStream := e.reactOptions(platform, owner, repo, number, question, qaRetryHint)
	defer closeStream()
	final, err := agentrun.Run(ctx, agentrun.Config{
		Name:             "argus-qa",
		DisableStreaming: e.DeltaSink == nil, // P3 opt-in：nil = 旧整段语义（回滚开关）
		Description:      "PR 问答助手",
		Instruction:      instruction,
		Model:            e.chatModel(),
		MaxIterations:    8,
		ToolsFactory: func() []tool.BaseTool {
			t, _, e2 := e.tools(ctx, platform, owner, repo, number)
			if e2 != nil {
				return nil
			}
			return t
		},
	}, question, opts...)
	if err != nil {
		return "", err
	}
	answer := strings.TrimSpace(final)
	if answer == "" {
		return "", fmt.Errorf("空回答")
	}
	return answer, nil
}

// reactOptions agentrun 运行选项：重试提示恒在；DeltaSink 非 nil 时再挂流式
// 收敛器（返回值 close 供调用方 defer 冲刷残尾）。关闭时选项与旧行为逐字一致。
func (e *Engine) reactOptions(platform, owner, repo, number, question, retryHint string) (
	[]agentrun.RunOption, func()) {
	opts := []agentrun.RunOption{agentrun.WithRetry(question + retryHint)}
	if e.DeltaSink == nil {
		return opts, func() {}
	}
	streamer := agentstream.New(func(d plugin.AgentDelta) {
		e.DeltaSink(platform, owner, repo, number, d)
	})
	opts = append(opts, agentrun.WithOnEvent(streamer.Handle))
	return opts, streamer.Close
}

// single 单轮直调（ReAct 的降级兜底；带历史上下文与 PR 摘要）。
func (e *Engine) single(ctx context.Context, platform, owner, repo, number, question, historyText string) (string, error) {
	prCtx, err := e.loadContext(ctx, platform, owner, repo, number)
	if err != nil {
		return "", fmt.Errorf("加载 PR 上下文失败: %w", err)
	}
	client := llm.NewClient(e.chatModel(), "", nil)
	msgs := []*schema.Message{
		schema.SystemMessage(systemPrompt("", historyText)),
		// v3.8.0：PR 标题/描述/作者（敌对输入）此前裸拼进 user message——
		// ReAct 失败时自动落回此路径，注入防线与故障率正相关倒挂。补围栏。
		schema.UserMessage(plugin.DataFence("PR 上下文", prCtx) + "\n\n用户问题: " + question),
	}
	resp, err := client.Generate(ctx, "qa", msgs)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
