package qa

// PTC 程序化取证路径测试：程序合成/执行/诊断回喂两轮 + 作答，全部用脚本化
// 假模型与假工具（无真实 LLM）；失败必须回退（handled=false）而非问答失败。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yi-nology/goagent-kernel/ptc"

	"github.com/yi-nology/argus/internal/plugin"
)

func TestPtcAskProgramSuccess(t *testing.T) {
	m := &fakeModel{contents: []string{"```js\nreturn \"证据\"\n```", "最终答案"}}
	e, _, _ := newTestEngine(t, m)

	ans, handled, err := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7",
		"登录逻辑在哪？", "历史对话", plugin.DataFence("报告", "R-1"))
	if err != nil || !handled {
		t.Fatalf("ptcAsk = %q, handled=%v, err=%v", ans, handled, err)
	}
	if ans != "最终答案" {
		t.Fatalf("answer = %q", ans)
	}
	if m.callCount() != 2 {
		t.Fatalf("应两轮 LLM（合成+作答），got %d", m.callCount())
	}
	// 作答轮的系统提示应带程序取证围栏与报告锚。
	sys := m.lastSystemText()
	if !strings.Contains(sys, "程序取证结果") || !strings.Contains(sys, "最近审查报告") {
		t.Fatalf("作答轮缺少证据/报告围栏:\n%s", sys)
	}
}

func TestPtcAskSynthFailureFallsBack(t *testing.T) {
	// 模型合成失败 → handled=false（回退 ReAct），不是问答错误。
	m := &fakeModel{errs: []error{errors.New("401 invalid api key")}}
	e, _, _ := newTestEngine(t, m)
	if _, handled, err := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", ""); handled || err != nil {
		t.Fatalf("合成失败应回退: handled=%v err=%v", handled, err)
	}

	// 模型没给程序（无围栏且无 host. 调用）→ 回退。
	m2 := &fakeModel{contents: []string{"我不知道"}}
	e2, _, _ := newTestEngine(t, m2)
	if _, handled, _ := e2.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", ""); handled {
		t.Fatal("空程序应回退")
	}
}

func TestPtcAskBudgetFailureNotRepaired(t *testing.T) {
	// 17 次 host 调用超 MaxCalls=16：预算类失败不重试（同型重试只再烧预算）。
	prog := "```js\nfor (let i = 0; i < 17; i++) { host.get_diff('{}') }\nreturn \"x\"\n```"
	m := &fakeModel{contents: []string{prog}}
	e, _, _ := newTestEngine(t, m)
	_, handled, err := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", "")
	if handled || err != nil {
		t.Fatalf("预算失败应直接回退: handled=%v err=%v", handled, err)
	}
	if m.callCount() != 1 {
		t.Fatalf("预算类失败不应回喂重试: calls=%d", m.callCount())
	}
}

func TestPtcAskRepairSuccess(t *testing.T) {
	bad := "```js\nhost.no_such_tool('{}')\n```" // runtime 错误 → 可修复
	fixed := "```js\nreturn \"修复后的证据\"\n```"
	m := &fakeModel{contents: []string{bad, fixed, "修复后的答案"}}
	e, _, _ := newTestEngine(t, m)

	ans, handled, err := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", "")
	if err != nil || !handled || ans != "修复后的答案" {
		t.Fatalf("修复路径失败: ans=%q handled=%v err=%v", ans, handled, err)
	}
	if m.callCount() != 3 {
		t.Fatalf("应合成+修复+作答三轮, got %d", m.callCount())
	}
}

func TestPtcAskRepairGivesUp(t *testing.T) {
	bad := "```js\nhost.no_such_tool('{}')\n```"
	// 修复轮返回同一程序 → 放弃（不重复执行同一失败程序）。
	m := &fakeModel{contents: []string{bad, bad}}
	e, _, _ := newTestEngine(t, m)
	if _, handled, _ := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", ""); handled {
		t.Fatal("修复无变化应回退")
	}
	if m.callCount() != 2 {
		t.Fatalf("应合成+修复两次 LLM, got %d", m.callCount())
	}
}

func TestPtcAskAnswerFailuresFallBack(t *testing.T) {
	prog := "```js\nreturn \"ok\"\n```"
	// 作答空 → 回退。
	m := &fakeModel{contents: []string{prog, ""}}
	e, _, _ := newTestEngine(t, m)
	if _, handled, _ := e.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", ""); handled {
		t.Fatal("空作答应回退")
	}

	// 作答模型错误 → 回退。
	m2 := &fakeModel{contents: []string{prog}, errs: []error{nil, errors.New("401 invalid api key")}}
	e2, _, _ := newTestEngine(t, m2)
	if _, handled, _ := e2.ptcAsk(context.Background(), "gitea", "acme", "app", "7", "q", "", ""); handled {
		t.Fatal("作答错误应回退")
	}
}

func TestBuildProgramPromptIncludesToolSurface(t *testing.T) {
	m := &fakeModel{}
	e, _, _ := newTestEngine(t, m)
	tools, strategy, err := e.tools(context.Background(), "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	sys := buildProgramPrompt("问题", "历史", "REPORT-ANCHOR", strategy, tools)
	if !strings.Contains(sys, "host.get_diff") || !strings.Contains(sys, "host.get_file") {
		t.Fatalf("程序提示缺工具面:\n%s", sys)
	}
	if !strings.Contains(sys, "必须有 return 语句") {
		t.Fatalf("程序提示缺 return 契约:\n%s", sys)
	}
	if !strings.Contains(sys, "REPORT-ANCHOR") || !strings.Contains(sys, "最近审查报告") {
		t.Fatalf("报告锚未入围栏:\n%s", sys)
	}
}

func ptcResult(kind string) ptc.Result { return ptc.Result{OK: false, ErrorKind: kind} }

func ptcResultOK(value string) ptc.Result { return ptc.Result{OK: true, Value: value} }

func TestPtcProgramFixableClassification(t *testing.T) {
	// 正确性类可修复；预算类不可；OK+null（忘写 return）按可修复处理。
	if !programFixable(ptcResult("syntax")) || !programFixable(ptcResult("runtime")) {
		t.Fatal("syntax/runtime 应可修复")
	}
	if programFixable(ptcResult("budget_calls")) || programFixable(ptcResult("deadline")) {
		t.Fatal("预算类不应重试")
	}
	if !programFixable(ptcResultOK("null")) {
		t.Fatal("OK 但 null（忘 return）应可修复")
	}
	if programFixable(ptcResultOK("\"v\"")) {
		t.Fatal("OK 且有值不应重试")
	}
}
