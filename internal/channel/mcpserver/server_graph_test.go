package mcpserver

import (
	"context"
	"strings"
	"testing"
)

// ---------- 符号图工具面（v5.9.0） ----------

// fakeGraphCore 在 fakeCore 之上实现符号图两接口（get_trace/get_symbol_context 数据面）。
type fakeGraphCore struct {
	fakeCore
	out string
	err error
}

func (f *fakeGraphCore) Trace(_ context.Context, _, _, _, _, _, _ string) (string, error) {
	return f.out, f.err
}

func (f *fakeGraphCore) SymContext(_ context.Context, _, _, _, _, _ string) (string, error) {
	return f.out, f.err
}

// 实现两接口后 get_trace/get_symbol_context 双双注册并可调用（v5.9.0）。
func TestNew_GraphToolsRegisteredAndCalled(t *testing.T) {
	core := &fakeGraphCore{out: "get_trace：from→to 可达，途经 2 跳"}
	srv := New(core, "v-test")
	names := listToolNames(t, srv)
	for _, want := range []string{"get_trace", "get_symbol_context"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("实现 GraphTracer/SymbolContexter 后应注册 %s（注册面: %v）", want, names)
		}
	}
	text, isErr := callTool(t, srv, "get_trace", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"from": "HandleAuth", "to": "refreshSession",
	})
	if isErr {
		t.Fatalf("get_trace 报错: %s", text)
	}
	if !strings.Contains(text, "get_trace") {
		t.Errorf("get_trace 回执缺失: %s", text)
	}
	text, isErr = callTool(t, srv, "get_symbol_context", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"symbol": "Handler.ServeHTTP",
	})
	if isErr {
		t.Fatalf("get_symbol_context 报错: %s", text)
	}
	if !strings.Contains(text, "可达") {
		t.Errorf("get_symbol_context 回执缺失: %s", text)
	}
}

// 必填缺失 → 工具级 error 回执（schema Required 的运行期兜底）。
func TestNew_GraphToolsMissingArgs(t *testing.T) {
	srv := New(&fakeGraphCore{out: "不应触达"}, "v-test")
	if _, isErr := callTool(t, srv, "get_trace", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "from": "A",
	}); !isErr {
		t.Error("get_trace 缺 to 应 isError")
	}
	if _, isErr := callTool(t, srv, "get_symbol_context", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app",
	}); !isErr {
		t.Error("get_symbol_context 缺 symbol 应 isError")
	}
}

// 未实现 GraphTracer/SymbolContexter 时 tools/list 不出现两工具（死工具纪律）。
func TestNew_GraphToolsNotRegisteredWhenUnimplemented(t *testing.T) {
	names := listToolNames(t, New(&fakeCore{}, "v-test"))
	for _, banned := range []string{"get_trace", "get_symbol_context"} {
		for _, n := range names {
			if n == banned {
				t.Errorf("core 未实现符号图接口却注册了 %s", banned)
			}
		}
	}
}
