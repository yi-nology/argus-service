package loopadapt

// loopadapt_test.go —— 共用接缝单测：einoChat 分支面、工具转换、事件桥
// （含 tool_call bytes 轨迹钩子）。react 级行为测试在 internal/qa。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/yi-nology/goagent-runtime/agentloop"
	"github.com/yi-nology/goagent-runtime/chat"
	"github.com/yi-nology/goagent-runtime/pipeline"

	"github.com/yi-nology/argus/internal/agentstream"
)

// ---------- 测试替身 ----------

// fakeModel Generate 队列：按次弹出回复/错误；耗尽粘住最后一项。Stream 恒拒。
type fakeModel struct {
	mu       sync.Mutex
	contents []string
	errs     []error
	calls    int
}

func (f *fakeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		if len(f.errs) > 1 {
			f.errs = f.errs[1:]
		}
		if err != nil {
			return nil, err
		}
	}
	if len(f.contents) == 0 {
		return &schema.Message{Role: schema.Assistant, Content: ""}, nil
	}
	c := f.contents[0]
	if len(f.contents) > 1 {
		f.contents = f.contents[1:]
	}
	return &schema.Message{Role: schema.Assistant, Content: c}, nil
}

func (f *fakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("fakeModel: Stream 未实现")
}

// streamFakeModel Stream 返回合成分片。
type streamFakeModel struct {
	fakeModel
	chunks []*schema.Message
}

func (f *streamFakeModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(f.chunks), nil
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

// ---------- einoChat 分支面 ----------

// TestEinoChat_GenerateError OnDelta nil 走 Generate，模型错误原样上抛。
func TestEinoChat_GenerateError(t *testing.T) {
	m := &fakeModel{errs: []error{fmt.Errorf("401 invalid api key")}}
	_, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
	})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Generate 错误应上抛: %v", err)
	}
}

// TestEinoChat_StreamZeroChunks 干净 EOF 零分片 = 传输侧断流，显式报错。
func TestEinoChat_StreamZeroChunks(t *testing.T) {
	m := &streamFakeModel{chunks: nil}
	_, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
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
	_, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(chat.Delta) {},
	})
	if err == nil || !strings.Contains(err.Error(), "网关断流") {
		t.Fatalf("Recv 错误应上抛: %v", err)
	}
}

// TestEinoChat_StreamCallError 模型 Stream 本身失败 → 原样上抛。
func TestEinoChat_StreamCallError(t *testing.T) {
	_, err := NewEinoChat(&fakeModel{}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(chat.Delta) {},
	})
	if err == nil || !strings.Contains(err.Error(), "Stream 未实现") {
		t.Fatalf("Stream 错误应上抛: %v", err)
	}
}

// TestEinoChat_ToolSpecBadParameters 参数模式非 JSON → 装配期解析错误。
func TestEinoChat_ToolSpecBadParameters(t *testing.T) {
	_, err := NewEinoChat(&fakeModel{}).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		Tools:    []chat.ToolSpec{{Name: "bad", Parameters: "{not json"}},
	})
	if err == nil || !strings.Contains(err.Error(), "参数模式解析失败") {
		t.Fatalf("坏参数模式应报错: %v", err)
	}
}

// TestEinoChat_ReasoningAndToolCallChunks reasoning 分片外发；带 tool_calls
// 的正文分片跳过；普通正文外发；调用轮回传保留。
func TestEinoChat_ReasoningAndToolCallChunks(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		{Role: schema.Assistant, ReasoningContent: "先想一下"},
		{Role: schema.Assistant, Content: "调用轮片段",
			ToolCalls: []schema.ToolCall{{ID: "c1", Function: schema.FunctionCall{Name: "get_file", Arguments: "{}"}}}},
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	resp, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
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

// TestEinoChat_StreamSkipsNilChunk nil 分片跳过。
func TestEinoChat_StreamSkipsNilChunk(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		nil,
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	resp, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
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

// TestEinoChat_StreamSkipsNonAssistant 非 assistant 分片不外发增量；与
// assistant 分片角色冲突 → 合并失败显式报错（跳过是增量面、错是消息面）。
func TestEinoChat_StreamSkipsNonAssistant(t *testing.T) {
	m := &streamFakeModel{chunks: []*schema.Message{
		{Role: schema.Tool, Content: "工具结果", ToolCallID: "c1"},
		{Role: schema.Assistant, Content: "终稿"},
	}}
	var got []chat.Delta
	_, err := NewEinoChat(m).Chat(context.Background(), chat.ChatRequest{
		Messages: []chat.Message{{Role: chat.RoleUser, Content: "q"}},
		OnDelta:  func(d chat.Delta) { got = append(got, d) },
	})
	if err == nil || !strings.Contains(err.Error(), "合并失败") {
		t.Fatalf("角色冲突合并应报错: %v", err)
	}
	if len(got) != 1 || got[0].Text != "终稿" {
		t.Fatalf("tool 分片不应产增量: %+v", got)
	}
}

// TestResponseFromNil 空消息 → 零值响应（不 panic）。
func TestResponseFromNil(t *testing.T) {
	if r := responseFrom(nil); r.Content != "" || len(r.ToolCalls) != 0 {
		t.Fatalf("nil 消息应零值: %+v", r)
	}
}

// ---------- 工具转换 ----------

// TestToPipelineTools_NonInvokable 无可执行体的工具装配期报错。
func TestToPipelineTools_NonInvokable(t *testing.T) {
	_, err := ToPipelineTools(context.Background(), []tool.BaseTool{infoOnlyTool{}}, SafeReadOnly)
	if err == nil || !strings.Contains(err.Error(), "非 Invokable") {
		t.Fatalf("非 Invokable 应报错: %v", err)
	}
}

// TestToPipelineTools_InfoError 元数据读取失败 → 装配错误上抛。
func TestToPipelineTools_InfoError(t *testing.T) {
	_, err := ToPipelineTools(context.Background(), []tool.BaseTool{infoErrTool{}}, SafeReadOnly)
	if err == nil || !strings.Contains(err.Error(), "元数据读取失败") {
		t.Fatalf("Info 错误应上抛: %v", err)
	}
}

// TestToPipelineTools_SafePassed Safe 断言透传（只读表 → pipeline.Tool.Safe）。
func TestToPipelineTools_SafePassed(t *testing.T) {
	inv := invokableStub{}
	tools, err := ToPipelineTools(context.Background(), []tool.BaseTool{inv}, SafeReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || !tools[0].Safe {
		t.Fatalf("SafeReadOnly 应透传: %+v", tools)
	}
	tools, err = ToPipelineTools(context.Background(), []tool.BaseTool{inv}, SafeMutating)
	if err != nil {
		t.Fatal(err)
	}
	if tools[0].Safe {
		t.Fatal("SafeMutating 应为 false")
	}
}

// infoOnlyTool 只有元数据、无可执行体。
type infoOnlyTool struct{}

func (infoOnlyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "meta-only", Desc: "d"}, nil
}

// infoErrTool Info 读取即错。
type infoErrTool struct{}

func (infoErrTool) Info(context.Context) (*schema.ToolInfo, error) {
	return nil, fmt.Errorf("info boom")
}

// invokableStub 可执行的最小工具。
type invokableStub struct{}

func (invokableStub) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stub", Desc: "d"}, nil
}

func (invokableStub) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

// ---------- 事件桥 ----------

// TestStreamEmitter_Dispatch content_delta 入收敛器、tool_call 触发轮次
// 推进、round/session 事件忽略。
func TestStreamEmitter_Dispatch(t *testing.T) {
	var got []agentstream.Delta
	s := agentstream.New(func(d agentstream.Delta) { got = append(got, d) })
	em := NewStreamEmitter(s)
	ctx := context.Background()
	if err := em.Publish(ctx, "s", "", nil, agentloop.EventRoundStart, "info", map[string]any{"round": 1}); err != nil {
		t.Fatal(err)
	}
	if err := em.Publish(ctx, "s", "", nil, agentloop.EventSessionState, "info", map[string]any{"status": "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("非流式事件应忽略: %+v", got)
	}
	if err := em.Publish(ctx, "s", "", nil, agentloop.EventContentDelta, "info",
		map[string]any{"kind": "text", "text": strings.Repeat("x", 250)}); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Phase != agentstream.PhaseText {
		t.Fatalf("content_delta 应入收敛器: %+v", got)
	}
	// tool_call：轮次推进（下一片增量进新轮）——收敛器行为由 agentstream
	// 自测覆盖，此处只验证事件路由不丢。
	if err := em.Publish(ctx, "s", "", nil, agentloop.EventToolCall, "info",
		map[string]any{"call_id": "c1", "tool": "get_file", "outcome": "applied"}); err != nil {
		t.Fatal(err)
	}
}

// TestTraceTool 轨迹装饰：执行后落 tool/bytes；错误轮也落（out 可能为空）；
// trace nil 时原样返回（不包 Handler）。
func TestTraceTool(t *testing.T) {
	var got []string
	var lastErr error
	trace := func(tool string, bytes int, d time.Duration, err error) {
		got = append(got, fmt.Sprintf("%s/%d", tool, bytes))
		lastErr = err
		if d < 0 {
			t.Errorf("耗时不应为负: %v", d)
		}
	}
	base := pipeline.Tool{Name: "get_file", Handler: func(context.Context, []byte) (string, error) {
		return "file-body", nil
	}}
	wrapped := TraceTool(base, trace)
	out, err := wrapped.Handler(context.Background(), []byte("{}"))
	if err != nil || out != "file-body" {
		t.Fatalf("执行体应透传: out=%q err=%v", out, err)
	}
	if len(got) != 1 || got[0] != "get_file/9" || lastErr != nil {
		t.Fatalf("轨迹应落 tool/bytes 且成功: %+v lastErr=%v", got, lastErr)
	}
	// 错误轮：结果空串 → bytes=0，错误原样上抛且轨迹带 err。
	errBase := pipeline.Tool{Name: "boom", Handler: func(context.Context, []byte) (string, error) {
		return "", fmt.Errorf("boom")
	}}
	if _, err := TraceTool(errBase, trace).Handler(context.Background(), nil); err == nil {
		t.Fatal("错误应透传")
	}
	if len(got) != 2 || got[1] != "boom/0" || lastErr == nil {
		t.Fatalf("错误轮轨迹 bytes=0 且带 err: %+v lastErr=%v", got, lastErr)
	}
	// trace nil：原样返回（Handler 不被包）。
	if TraceTool(base, nil).Handler == nil {
		t.Fatal("nil trace 应保留原 Handler")
	}
}
