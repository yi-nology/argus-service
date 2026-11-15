package mcpserver

import (
	"strings"
	"testing"
)

// ---------- ask_argus ----------

func TestAskArgus_NotEnabled(t *testing.T) {
	srv := New(&fakeCore{}, "v-test") // 未实现 ReviewQA
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr || !strings.Contains(text, "未启用") {
		t.Fatalf("未实现 ReviewQA 应报未启用: isErr=%v text=%s", isErr, text)
	}
}

func TestAskArgus_RequiredAndNumberValidation(t *testing.T) {
	qa := &fakeQA{}
	srv := New(qa, "v-test")

	// 缺 platform
	_, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr {
		t.Fatal("缺 platform 应 isError")
	}

	// number 非纯数字（与 service.isDigits 同口径）
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7a",
	})
	if !isErr || !strings.Contains(text, "纯数字") {
		t.Fatalf("非数字 number 应拒绝: isErr=%v text=%s", isErr, text)
	}
}

func TestAskArgus_LatestAndAnswer(t *testing.T) {
	qa := &fakeQA{latest: map[string]any{"verdict": "approve"}, latestOK: true}
	srv := New(qa, "v-test")

	// 无 question → 最近审查结论
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if isErr {
		t.Fatalf("LatestReview 不应 isError: %s", text)
	}
	if !strings.Contains(text, "approve") {
		t.Fatalf("应返回最近结论:\n%s", text)
	}

	// 无审查记录
	qa.latestOK = false
	text, _ = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !strings.Contains(text, "尚无已完成") {
		t.Fatalf("应提示无记录:\n%s", text)
	}

	// 带 question → AnswerAboutReview
	qa.answer, qa.answerOK = "因为证据在 a.go:12", true
	text, isErr = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"question": "为什么这条是 high",
	})
	if isErr || !strings.Contains(text, "a.go:12") {
		t.Fatalf("追问应返回答案: isErr=%v text=%s", isErr, text)
	}
	if qa.askedQ != "为什么这条是 high" || qa.askedNum != "7" {
		t.Fatalf("追问参数未透传: q=%q num=%q", qa.askedQ, qa.askedNum)
	}

	// 答不上来
	qa.answerOK = false
	text, _ = callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"question": "随便问",
	})
	if !strings.Contains(text, "无法回答") {
		t.Fatalf("答不上来应有明确提示:\n%s", text)
	}
}

// ---------- 副驾三工具 ----------

func TestCopilotTools(t *testing.T) {
	cop := &fakeCopilot{
		stages:     map[string]any{"task_id": "t-1", "stages": []any{"R1", "R5"}},
		stagesOK:   true,
		findings:   []map[string]any{{"fp": "ab12cd34ef", "severity": "high"}},
		findingsOK: true,
		suggestion: map[string]any{"suggested_fix": "加锁"},
		suggOK:     true,
	}
	srv := New(cop, "v-test")

	// get_task_stages
	text, isErr := callTool(t, srv, "get_task_stages", map[string]any{"task_id": "t-1"})
	if isErr || !strings.Contains(text, "R1") {
		t.Fatalf("stages: isErr=%v text=%s", isErr, text)
	}
	cop.stagesOK = false
	text, isErr = callTool(t, srv, "get_task_stages", map[string]any{"task_id": "nope"})
	if !isErr || !strings.Contains(text, "任务不存在") {
		t.Fatalf("stages 未命中应 isError: isErr=%v text=%s", isErr, text)
	}
	cop.stagesOK = true

	// list_findings
	text, isErr = callTool(t, srv, "list_findings", map[string]any{"task_id": "t-1"})
	if isErr || !strings.Contains(text, "ab12cd34ef") {
		t.Fatalf("findings: isErr=%v text=%s", isErr, text)
	}

	// get_suggestion：fp 长度门（6-64）
	_, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "abc"})
	if !isErr {
		t.Fatal("过短 fp 应 isError")
	}
	// 命中（大小写归一为小写）
	text, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "AB12CD34EF"})
	if isErr || !strings.Contains(text, "加锁") {
		t.Fatalf("suggestion 命中: isErr=%v text=%s", isErr, text)
	}
	if cop.suggFP != "ab12cd34ef" {
		t.Fatalf("fp 未归一小写: %s", cop.suggFP)
	}
	// 未命中 → 明确文本（非 isError）
	cop.suggOK = false
	text, isErr = callTool(t, srv, "get_suggestion", map[string]any{"task_id": "t-1", "fp": "deadbeef12"})
	if isErr || !strings.Contains(text, "未命中") {
		t.Fatalf("suggestion 未命中: isErr=%v text=%s", isErr, text)
	}
}

// TestAskArgus_QuestionCap 追问长度上限（v3.134.0 成本/DoS 约束）。
func TestAskArgus_QuestionCap(t *testing.T) {
	core := &fakeQA{answer: "x", answerOK: true}
	srv := New(core, "v-test")
	long := make([]byte, maxQuestionRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	text, isErr := callTool(t, srv, "ask_argus", map[string]any{
		"platform": "gitlab", "owner": "acme", "repo": "app", "number": "1", "question": string(long),
	})
	if !isErr || !strings.Contains(text, "过长") {
		t.Errorf("超长 question 应报错: isErr=%v text=%s", isErr, text[:60])
	}
}
