// react_test.go —— ReAct 运行接线单源（loopadapt.RunReAct）：成功/回喂重试/
// 双败上抛/结果判定（含 unknown 禁重试文案）。
package loopadapt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"

	"github.com/yi-nology/goagent-runtime/agentloop"
	"github.com/yi-nology/goagent-runtime/chat"
)

func TestResultError(t *testing.T) {
	cases := []struct {
		name string
		res  *agentloop.Result
		want string
	}{
		{"nil 结果", nil, "无结果"},
		{"未知态", &agentloop.Result{Status: "succeeded", Final: "x", Uncertain: []string{"c1", "c2"}}, "未知态工具调用（c1,c2）"},
		{"非成功", &agentloop.Result{Status: "failed"}, "未达终态: failed"},
		{"空终答", &agentloop.Result{Status: "succeeded", Final: "  "}, "空回答"},
		{"可用", &agentloop.Result{Status: "succeeded", Final: "ok"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := resultError("T", tc.res)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("应可用, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want contains %q, got %v", tc.want, err)
			}
		})
	}
}

func TestRunReActSuccess(t *testing.T) {
	m := &fakeModel{contents: []string{"answer"}}
	var finals []string
	var stats ReActStats
	toolCalls := 0
	got, err := RunReAct(context.Background(), ReActSpec{
		SessionPrefix: "t",
		Model:         func() ChatModel { return NewEinoChat(m) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			toolCalls++
			return []tool.BaseTool{invokableStub{}}, nil
		},
		System:    "sys",
		MaxRounds: 3,
		Label:     "T",
		OnFinal:   func(f string) { finals = append(finals, f) },
		OnStats:   func(s ReActStats) { stats = s },
	}, "hi")
	if err != nil || got != "answer" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if toolCalls != 1 {
		t.Fatalf("成功不应重试，tools 调用 %d", toolCalls)
	}
	if len(finals) != 1 || finals[0] != "answer" {
		t.Fatalf("OnFinal 应收到终答: %v", finals)
	}
	if stats.Attempts != 1 || stats.Rounds < 1 || stats.ToolCalls != 0 ||
		stats.Uncertain != 0 || stats.Status != "succeeded" {
		t.Fatalf("遥测不符: %+v", stats)
	}
}

func TestRunReActRetryOnEmptyFinal(t *testing.T) {
	m := &fakeModel{contents: []string{"", "answer"}}
	toolCalls, resets := 0, 0
	var stats ReActStats
	got, err := RunReAct(context.Background(), ReActSpec{
		SessionPrefix: "t",
		Model:         func() ChatModel { return NewEinoChat(m) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			toolCalls++
			return []tool.BaseTool{invokableStub{}}, nil
		},
		MaxRounds: 3,
		RetryHint: "retry",
		Label:     "T",
		OnReset:   func() { resets++ },
		OnStats:   func(s ReActStats) { stats = s },
	}, "hi")
	if err != nil || got != "answer" {
		t.Fatalf("空终答应回喂重试成功: got=%q err=%v", got, err)
	}
	if toolCalls != 2 || resets != 1 {
		t.Fatalf("tools=%d resets=%d, want 2/1", toolCalls, resets)
	}
	if stats.Attempts != 2 || stats.Rounds < 2 || stats.Status != "succeeded" {
		t.Fatalf("重试遥测不符: %+v", stats)
	}
}

func TestRunReActRetryOnErrorThenSuccess(t *testing.T) {
	m := &fakeModel{errs: []error{errors.New("boom"), nil}, contents: []string{"answer"}}
	toolCalls := 0
	got, err := RunReAct(context.Background(), ReActSpec{
		SessionPrefix: "t",
		Model:         func() ChatModel { return NewEinoChat(m) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			toolCalls++
			return []tool.BaseTool{invokableStub{}}, nil
		},
		MaxRounds: 3,
		RetryHint: "retry",
		Label:     "T",
	}, "hi")
	if err != nil || got != "answer" {
		t.Fatalf("错误后重试应成功: got=%q err=%v", got, err)
	}
	if toolCalls != 2 {
		t.Fatalf("tools=%d, want 2", toolCalls)
	}
}

func TestRunReActBothAttemptsFail(t *testing.T) {
	m := &fakeModel{errs: []error{errors.New("boom1"), errors.New("boom2")}}
	toolCalls, resets := 0, 0
	var stats ReActStats
	_, err := RunReAct(context.Background(), ReActSpec{
		SessionPrefix: "t",
		Model:         func() ChatModel { return NewEinoChat(m) },
		Tools: func(context.Context) ([]tool.BaseTool, error) {
			toolCalls++
			return []tool.BaseTool{invokableStub{}}, nil
		},
		MaxRounds: 3,
		RetryHint: "retry",
		Label:     "T",
		OnReset:   func() { resets++ },
		OnStats:   func(s ReActStats) { stats = s },
	}, "hi")
	if err == nil || !strings.Contains(err.Error(), "T 运行失败") {
		t.Fatalf("双败应上抛运行失败: %v", err)
	}
	if toolCalls != 2 || resets != 1 {
		t.Fatalf("tools=%d resets=%d, want 2/1", toolCalls, resets)
	}
	if stats.Attempts != 2 || stats.Status != "error" {
		t.Fatalf("双败遥测不符: %+v", stats)
	}
}

func TestRunReActUnassembled(t *testing.T) {
	if _, err := RunReAct(context.Background(), ReActSpec{Label: "T"}, "hi"); err == nil ||
		!strings.Contains(err.Error(), "未装配") {
		t.Fatalf("模型/工具缺装配应报错: %v", err)
	}
}

// 编译面：ChatModel 别名确实等于 runtime chat.Model。
var _ ChatModel = chat.Model(nil)
