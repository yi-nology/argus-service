package qa

// adapter_agentloop_test.go —— QA 迁 agentloop 的工具回路：模型要工具 →
// pipeline 施加（只读 get_diff 走 fakePlatform）→ tool 消息回喂带
// loop 铸造的调用 ID 与执行结果 → 终答收口。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yi-nology/goagent-runtime/agentloop"
	"github.com/yi-nology/goagent-runtime/chat"

	"github.com/yi-nology/argus/internal/agentstream"
	"github.com/yi-nology/argus/internal/plugin"
)

// toolCallFakeModel 首次 Generate 返回带 ToolCalls 的调用轮，其后返回终答；
// 记录每次入参供回喂断言。
type toolCallFakeModel struct {
	mu        sync.Mutex
	calls     int
	lastMsgs  []*schema.Message
	toolCalls []schema.ToolCall
	final     string
}

func (f *toolCallFakeModel) Generate(_ context.Context, msgs []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastMsgs = msgs
	if f.calls == 1 {
		return &schema.Message{Role: schema.Assistant, ToolCalls: f.toolCalls}, nil
	}
	return &schema.Message{Role: schema.Assistant, Content: f.final}, nil
}

func (f *toolCallFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("toolCallFakeModel: Stream 未实现")
}

func (f *toolCallFakeModel) snapshot() (int, []*schema.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.lastMsgs
}

// TestReact_ToolCallRoundtrip 调用轮回路：模型请求 get_diff → agentloop
// 铸 ID、pipeline 执行（fakePlatform 真 diff）→ 第二次 Generate 收到
// tool 角色回喂（ID 非空、结果含 diff 文本）→ 终答原样返回。
func TestReact_ToolCallRoundtrip(t *testing.T) {
	m := &toolCallFakeModel{
		toolCalls: []schema.ToolCall{{
			ID: "model-made-id", Type: "function",
			Function: schema.FunctionCall{Name: "get_diff", Arguments: "{}"},
		}},
		final: "带证据的答案",
	}
	e, _, _ := newTestEngine(t, m)
	ans, err := e.react(context.Background(), "gitea", "acme", "app", "7", "diff 里改了什么", "", "")
	if err != nil {
		t.Fatalf("react: %v", err)
	}
	if ans != "带证据的答案" {
		t.Fatalf("answer = %q", ans)
	}
	calls, msgs := m.snapshot()
	if calls != 2 {
		t.Fatalf("应两次 Generate（调用轮+终答）: %d", calls)
	}
	var toolMsg *schema.Message
	for i := range msgs {
		if msgs[i].Role == schema.Tool {
			toolMsg = msgs[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("第二次请求应含 tool 回喂消息: %+v", msgs)
	}
	// 调用 ID 由 loop 铸造（chat 契约：不是模型编的）——非空且回填到位。
	if toolMsg.ToolCallID == "" {
		t.Fatal("tool 消息应回填调用 ID")
	}
	// 执行结果来自 fakePlatform 的真 diff（get_diff 管道全链通）。
	if !strings.Contains(toolMsg.Content, "diff --git") {
		t.Fatalf("tool 回喂应含 diff 执行结果: %q", toolMsg.Content)
	}
}

// ---------- einoChat 分支面 ----------

// TestEinoChat_GenerateError OnDelta nil 走 Generate，模型错误原样上抛。
func TestEinoChat_GenerateError(t *testing.T) {
	m := &fakeModel{errs: []error{fmt.Errorf("401 invalid api key")}}
	_, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Generate 错误应上抛: %v", err)
	}
}

// TestEinoChat_StreamZeroChunks 干净 EOF 零分片 = 传输侧断流，显式报错
// （不放大成空成功——agentrun 同款两类成因分离）。
func TestEinoChat_StreamZeroChunks(t *testing.T) {
	m := &streamFakeModel{chunks: nil}
	_, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(chat.Delta) {},
	})
	if err == nil || !strings.Contains(err.Error(), "零分片") {
		t.Fatalf("零分片应报错: %v", err)
	}
}

// TestEinoChat_StreamRecvError 流中错误（非 EOF）上抛且路径关流恰好一次。
func TestEinoChat_StreamRecvError(t *testing.T) {
	m := &pipeErrModel{err: fmt.Errorf("网关断流")}
	_, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(chat.Delta) {},
	})
	if err == nil || !strings.Contains(err.Error(), "网关断流") {
		t.Fatalf("Recv 错误应上抛: %v", err)
	}
}

// pipeErrModel Stream 经 Pipe 注入：先一片正文、再错误收尾。
type pipeErrModel struct {
	fakeModel
	err error
}

func (f *pipeErrModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		sw.Send(&schema.Message{Role: schema.Assistant, Content: "半截"}, nil)
		sw.Send(nil, f.err)
	}()
	return sr, nil
}

// TestEinoChat_ToolSpecBadParameters 参数模式非 JSON → 装配期解析错误。
func TestEinoChat_ToolSpecBadParameters(t *testing.T) {
	_, err := (einoChat{m: &fakeModel{}}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		Tools:    []chat.ToolSpec{{Name: "bad", Parameters: "{not json"}},
	})
	if err == nil || !strings.Contains(err.Error(), "参数模式解析失败") {
		t.Fatalf("坏参数模式应报错: %v", err)
	}
}

// TestEinoChat_ReasoningAndToolCallChunks reasoning 分片外发；带 tool_calls
// 的正文分片跳过（调用轮拼接物非可读答复）；普通正文外发。
func TestEinoChat_ReasoningAndToolCallChunks(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "先想一下"},
		{Role: schema.Assistant, Content: "调用轮片段",
			ToolCalls: []schema.ToolCall{{ID: "c1", Function: schema.FunctionCall{Name: "get_file", Arguments: "{}"}}}},
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	resp, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(d chat.Delta) { got = append(got, d) },
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(got) != 2 || got[0].Kind != chat.DeltaKindReasoning || got[0].Text != "先想一下" ||
		got[1].Kind != chat.DeltaKindText || got[1].Text != "终稿" {
		t.Fatalf("增量序列错: %+v", got)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "get_file" {
		t.Fatalf("调用轮回传应保留: %+v", resp.ToolCalls)
	}
}

// TestResponseFromNil 空消息 → 零值响应（不 panic）。
func TestResponseFromNil(t *testing.T) {
	if r := responseFrom(nil); r.Content != "" || len(r.ToolCalls) != 0 {
		t.Fatalf("nil 消息应零值: %+v", r)
	}
}

// TestToPipelineTools_NonInvokable 无可执行体的工具装配期报错（pipeline
// 没有第二执行形态）。
func TestToPipelineTools_NonInvokable(t *testing.T) {
	_, err := toPipelineTools(context.Background(), []tool.BaseTool{infoOnlyTool{}})
	if err == nil || !strings.Contains(err.Error(), "非 Invokable") {
		t.Fatalf("非 Invokable 应报错: %v", err)
	}
}

// infoOnlyTool 只有元数据、无可执行体。
type infoOnlyTool struct{}

func (infoOnlyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "meta-only", Desc: "d"}, nil
}

// TestReactEmitter_IgnoresNonStreamEvents round/session 事件与 SSE 流无关
// （忽略不产增量）；content_delta 照常入收敛器。
func TestReactEmitter_IgnoresNonStreamEvents(t *testing.T) {
	var got []agentstream.Delta
	s := agentstream.New(func(d agentstream.Delta) { got = append(got, d) })
	e := reactEmitter{s: s}
	ctx := context.Background()
	if err := e.Publish(ctx, "s", "", nil, agentloop.EventRoundStart, "info", map[string]any{"round": 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.Publish(ctx, "s", "", nil, agentloop.EventSessionState, "info", map[string]any{"status": "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("非流式事件不应产增量: %+v", got)
	}
	if err := e.Publish(ctx, "s", "", nil, agentloop.EventContentDelta, "info",
		map[string]any{"kind": "text", "text": strings.Repeat("x", 250)}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Phase != agentstream.PhaseText {
		t.Fatalf("content_delta 应入收敛器: %+v", got)
	}
}

// TestEinoChat_StreamCallError 模型 Stream 本身失败 → 原样上抛。
func TestEinoChat_StreamCallError(t *testing.T) {
	_, err := (einoChat{m: &fakeModel{}}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(chat.Delta) {},
	})
	if err == nil || !strings.Contains(err.Error(), "Stream 未实现") {
		t.Fatalf("Stream 错误应上抛: %v", err)
	}
}

// TestEinoChat_StreamSkipsNilChunk nil 分片跳过（不参与合并、不产增量）。
func TestEinoChat_StreamSkipsNilChunk(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		nil,
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	resp, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(d chat.Delta) { got = append(got, d) },
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(got) != 1 || got[0].Text != "终稿" {
		t.Fatalf("仅 assistant 正文外发: %+v", got)
	}
	if resp.Content == "" {
		t.Fatal("合并结果应含正文")
	}
}

// TestEinoChat_StreamSkipsNonAssistant 非 assistant 分片（tool 结果流）
// 不外发增量；与 assistant 分片角色冲突 → 合并失败显式报错（两类成因
// 分开：跳过是增量面，合并错是消息面）。
func TestEinoChat_StreamSkipsNonAssistant(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		{Role: schema.Tool, Content: "工具结果", ToolCallID: "c1"},
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	_, err := (einoChat{m: m}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(d chat.Delta) { got = append(got, d) },
	})
	if err == nil || !strings.Contains(err.Error(), "合并失败") {
		t.Fatalf("角色冲突合并应报错: %v", err)
	}
	// tool 分片被跳过不产增量；assistant 分片在合并前已外发（增量面与
	// 消息面解耦——合并失败不回滚已发增量，宿主以错误收口）。
	if len(got) != 1 || got[0].Text != "终稿" {
		t.Fatalf("tool 分片不应产增量: %+v", got)
	}
}

// TestToPipelineTools_InfoError 元数据读取失败 → 装配错误上抛。
func TestToPipelineTools_InfoError(t *testing.T) {
	_, err := toPipelineTools(context.Background(), []tool.BaseTool{infoErrTool{}})
	if err == nil || !strings.Contains(err.Error(), "元数据读取失败") {
		t.Fatalf("Info 错误应上抛: %v", err)
	}
}

type infoErrTool struct{}

func (infoErrTool) Info(context.Context) (*schema.ToolInfo, error) {
	return nil, fmt.Errorf("info boom")
}

// TestReact_FirstAttemptFailResetsStream DeltaSink 非 nil 且首跑失败：
// 回喂重试触发 OnReset（消费方收到 reset 相位，旧增量作废），重试仍失败
// → react 整体上抛。
func TestReact_FirstAttemptFailResetsStream(t *testing.T) {
	// Stream 恒拒的 fakeModel：OnDelta 挂载后模型走 Stream 分支必失败。
	m := &fakeModel{}
	e, _, _ := newTestEngine(t, m)
	var deltas []plugin.AgentDelta
	e.DeltaSink = func(_ context.Context, _, _, _, _ string, d plugin.AgentDelta) {
		deltas = append(deltas, d)
	}
	if _, err := e.react(context.Background(), "gitea", "acme", "app", "7", "问题", "", ""); err == nil {
		t.Fatal("双跑皆失败应上抛")
	}
	var sawReset bool
	for _, d := range deltas {
		if d.Phase == agentstream.PhaseReset {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatalf("首跑失败回喂重试应发 reset: %+v", deltas)
	}
}
