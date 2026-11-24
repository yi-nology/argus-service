package qa

// adapter_agentloop_test.go —— QA 迁 agentloop 的 react 级行为：工具回路
// （loop 铸 ID / pipeline 施加 / tool 回喂）与首跑失败回喂重试（OnReset）。
// einoChat/工具转换/事件桥的单测在 internal/loopadapt（共用接缝单源）。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

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
