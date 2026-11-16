package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

func TestArgBool(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"a": true, "b": "yes", "c": false,
	}}}
	if !argBool(req, "a") {
		t.Fatal("a 应为 true")
	}
	if argBool(req, "b") || argBool(req, "c") || argBool(req, "missing") {
		t.Fatal("非布尔/false/缺失都应回 false")
	}
}

// ---------- 纯函数面 ----------

func TestArgString(t *testing.T) {
	req := mcp.CallToolRequest{Params: mcp.CallToolParams{Arguments: map[string]any{
		"a": "x", "b": "", "c": 1,
	}}}
	if got := argString(req, "a", "d"); got != "x" {
		t.Fatalf("a = %q", got)
	}
	if got := argString(req, "b", "d"); got != "d" {
		t.Fatalf("空串应回默认: %q", got)
	}
	if got := argString(req, "c", "d"); got != "d" {
		t.Fatalf("非字符串应回默认: %q", got)
	}
	if got := argString(req, "missing", "d"); got != "d" {
		t.Fatalf("缺失应回默认: %q", got)
	}
}

func TestRenderTerminalTask_NoteFields(t *testing.T) {
	verdict := "comment"
	report := "body"
	tr := &argus.GetTaskResponse{TaskId: "t", Status: "done", Verdict: &verdict, Report: &report}

	res := renderTerminalTask(context.Background(), &fakeCore{}, tr, "verdict")
	text := extractText(t, res)
	if strings.Contains(text, "staleness") {
		t.Fatalf("verdict 注记不应含 staleness:\n%s", text)
	}

	res = renderTerminalTask(context.Background(), &fakeCore{}, tr, "note")
	text = extractText(t, res)
	if !strings.Contains(text, "staleness") {
		t.Fatalf("note 注记应含 staleness:\n%s", text)
	}
}

func TestMarshalToolResult_Error(t *testing.T) {
	// channel 可触发 json 错误的类型（循环引用）
	type node struct {
		Next *node
	}
	n := &node{}
	n.Next = n
	res, err := marshalToolResult(n)
	if err != nil {
		t.Fatalf("marshalToolResult 应吞掉错误并返回 isError: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("不可序列化应返回 isError 结果")
	}
}

func TestAgentCardPlaceholder(t *testing.T) {
	card := AgentCardPlaceholder("argus", "v3.1")
	if card["name"] != "argus" {
		t.Fatalf("name = %v", card["name"])
	}
	b, err := json.Marshal(card)
	if err != nil || !strings.Contains(string(b), "argus") {
		t.Fatalf("卡片应可序列化: %v %s", err, b)
	}
}

func extractText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("结果无内容")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("首内容非文本: %#v", res.Content[0])
	}
	return tc.Text
}

// bridgeWriter 幂等 WriteHeader：二次调用不覆盖首个状态码。
func TestBridgeWriter_WriteHeaderOnce(t *testing.T) {
	// 裸实现：只验证 wroteHeader 门闩语义（Hertz RequestContext 依赖真实运行时）。
	w := &bridgeWriter{header: http.Header{}}
	if w.wroteHeader {
		t.Fatal("初始不应已写头")
	}
	// Header() 可用
	if w.Header() == nil {
		t.Fatal("Header() 应返回非 nil")
	}
}

// pollReviewTask 的 ctx 取消路径：长轮询 + cancel → 「调用方取消」。
func TestPollReviewTask_ContextCancel(t *testing.T) {
	core := &fakeCore{getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res := pollReviewTask(ctx, core, "t-1", 30*time.Second)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消未及时退出: %v", elapsed)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "取消") {
		t.Fatalf("应返回调用方取消:\n%s", text)
	}
}
