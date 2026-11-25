package qa

import (
	"context"
	"errors"
	"testing"

	"github.com/oagentkit/guardx/sandbox"
	"github.com/oagentkit/goagent-kernel/ptc"
)

// runPTCProgram 经 sandbox 登记簿执行：绑定调用、返回值与诊断字段都要与
// 改造前的 ptc.Run 直调一致（provider 的 Detail 回传是这条链的关键）。
func TestRunPTCProgramThroughSandbox(t *testing.T) {
	bindings := []ptc.Binding{{
		Name: "sum",
		Call: func(context.Context, string) (string, error) { return `{"v":7}`, nil },
	}}
	res := runPTCProgram(context.Background(),
		`const r = JSON.parse(host.sum("{}")); return r.v;`, bindings)
	if !res.OK {
		t.Fatalf("程序应成功: kind=%s err=%s", res.ErrorKind, res.Error)
	}
	if res.Value != "7" {
		t.Fatalf("Value = %q，期望 7", res.Value)
	}
	if res.Calls != 1 {
		t.Fatalf("Calls = %d，期望 1", res.Calls)
	}
}

// 语法错误走 sandbox 入口时仍带 syntax 分类（programFixable 依赖它回喂）。
func TestRunPTCProgramSyntaxError(t *testing.T) {
	res := runPTCProgram(context.Background(), `return (`, nil)
	if res.OK {
		t.Fatal("语法错误不应成功")
	}
	if res.ErrorKind != "syntax" {
		t.Fatalf("ErrorKind = %q，期望 syntax", res.ErrorKind)
	}
}

// fakeProvider 只用于钉住 executePTC 的兜底形状（Detail 缺失 / Outcome 失败 /
// 登记簿选不到后端），不承担真实执行。
type fakeProvider struct {
	out sandbox.Outcome
}

func (fakeProvider) ID() string           { return "fake" }
func (fakeProvider) Level() sandbox.Level { return sandbox.LevelProcess }
func (f fakeProvider) Exec(context.Context, sandbox.Request) (sandbox.Outcome, error) {
	return f.out, nil
}

func TestExecutePTCFallbackShapes(t *testing.T) {
	ctx := context.Background()

	// Detail 不是 ptc.Result：至少把可判定文本交回调用方。
	reg := sandbox.NewRegistry()
	if err := reg.Register(fakeProvider{out: sandbox.Outcome{OK: true, Value: "v"}}); err != nil {
		t.Fatal(err)
	}
	res := executePTC(ctx, reg, "return 1")
	if !res.OK || res.Value != "v" {
		t.Fatalf("fallback 成功形态 = %+v", res)
	}

	// Outcome 失败且带 Err：分类归 sandbox、消息保留。
	reg2 := sandbox.NewRegistry()
	if err := reg2.Register(fakeProvider{out: sandbox.Outcome{OK: false, Err: errors.New("boom")}}); err != nil {
		t.Fatal(err)
	}
	res2 := executePTC(ctx, reg2, "return 1")
	if res2.OK || res2.ErrorKind != "sandbox" || res2.Error != "boom" {
		t.Fatalf("fallback 失败形态 = %+v", res2)
	}

	// 空登记簿：选不到后端 ⇒ 判不了就不放行（回退 ReAct）。
	res3 := executePTC(ctx, sandbox.NewRegistry(), "return 1")
	if res3.OK || res3.ErrorKind != "sandbox" {
		t.Fatalf("空登记簿形态 = %+v", res3)
	}
}
