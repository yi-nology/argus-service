package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ---------- list_agents / get_report ----------

func TestListAgents_JSON(t *testing.T) {
	core := &fakeCore{agents: &argus.ListAgentsResponse{
		Agents: []*argus.AgentManifestDTO{{Name: "security", Kind: argus.AgentKind_BUILTIN}},
	}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "list_agents", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app",
	})
	if isErr {
		t.Fatalf("list_agents 报错: %s", text)
	}
	if !strings.Contains(text, "security") {
		t.Fatalf("list_agents 缺插件名:\n%s", text)
	}
}

func TestGetReport_FieldsAndFailure(t *testing.T) {
	core := &fakeCore{getResp: &argus.GetTaskResponse{
		TaskId: "t-9", Status: "done",
		Verdict:    strPtr("request_changes"),
		Report:     strPtr("# 报告正文"),
		FailReason: strPtr("某阶段失败"),
	}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "get_report", map[string]any{"task_id": "t-9"})
	if isErr {
		t.Fatalf("get_report 报错: %s", text)
	}
	for _, want := range []string{"t-9", "request_changes", "报告正文", "某阶段失败"} {
		if !strings.Contains(text, want) {
			t.Fatalf("get_report 缺 %q:\n%s", want, text)
		}
	}

	// 缺必填 → isError
	_, isErr = callTool(t, srv, "get_report", map[string]any{})
	if !isErr {
		t.Fatal("缺 task_id 应返回 isError")
	}

	// 查询失败（非 200）
	core.getCode = http.StatusNotFound
	text, isErr = callTool(t, srv, "get_report", map[string]any{"task_id": "nope"})
	if !isErr || !strings.Contains(text, "查询失败") {
		t.Fatalf("非 200 应查询失败: isErr=%v text=%s", isErr, text)
	}
}

// ---------- review_pr / requirement_compliance ----------

func TestRunReview_MissingArgs(t *testing.T) {
	srv := New(&fakeCore{}, "v-test")
	_, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", // 缺 repo/number
	})
	if !isErr {
		t.Fatal("缺必填参数应 isError")
	}
}

func TestRunReview_CreateFail(t *testing.T) {
	core := &fakeCore{createResp: &argus.CreateTaskResponse{}, createCode: http.StatusBadRequest}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": false,
	})
	if !isErr || !strings.Contains(text, "创建任务失败") {
		t.Fatalf("创建失败应报错: isErr=%v text=%s", isErr, text)
	}
}

func TestRunReview_NoWaitReturnsTask(t *testing.T) {
	core := &fakeCore{createResp: &argus.CreateTaskResponse{TaskId: "t-42", Status: "queued"}, createCode: http.StatusOK}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": false,
	})
	if isErr {
		t.Fatalf("wait=false 不应报错: %s", text)
	}
	if !strings.Contains(text, "t-42") || !strings.Contains(text, "queued") {
		t.Fatalf("wait=false 应返回 task_id+status:\n%s", text)
	}
	if core.createKind != "requirement_compliance" {
		t.Fatalf("默认 kind = %s", core.createKind)
	}
	// kind 透传
	_, _ = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"kind": "code_review", "wait": false,
	})
	if core.createKind != "code_review" {
		t.Fatalf("kind 未透传: %s", core.createKind)
	}
}

func TestRunReview_WaitTerminalStates(t *testing.T) {
	verdict := "approve"
	report := "## 报告"

	// done → renderTerminalTask(noteField=verdict)
	core := &fakeCore{
		createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "done", Verdict: &verdict, Report: &report},
	}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if isErr {
		t.Fatalf("done 态不应 isError: %s", text)
	}
	for _, want := range []string{"done", "approve", "报告"} {
		if !strings.Contains(text, want) {
			t.Fatalf("done 态缺 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "staleness") {
		t.Fatalf("done 态不应带 staleness note:\n%s", text)
	}

	// stale → 带 note
	core.getResp = &argus.GetTaskResponse{TaskId: "t-1", Status: "stale", Verdict: &verdict, Report: &report}
	text, isErr = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if isErr {
		t.Fatalf("stale 态不应 isError: %s", text)
	}
	if !strings.Contains(text, "staleness") || !strings.Contains(text, "note") {
		t.Fatalf("stale 态应带 staleness note:\n%s", text)
	}

	// failed → isError + reason
	reason := "模型超时"
	core.getResp = &argus.GetTaskResponse{TaskId: "t-1", Status: "failed", FailReason: &reason}
	text, isErr = callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 5.0,
	})
	if !isErr || !strings.Contains(text, "审查失败") || !strings.Contains(text, "模型超时") {
		t.Fatalf("failed 态应报错带原因: isErr=%v text=%s", isErr, text)
	}
}

func TestRunReview_WaitTimeout(t *testing.T) {
	// 首查即非终态 + timeout 极小 → deadline 后返回超时提示（非 isError）
	core := &fakeCore{
		createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK,
		getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"},
	}
	srv := New(core, "v-test")
	start := time.Now()
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"wait": true, "timeout_seconds": 0.01,
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时路径耗时过长: %v", elapsed)
	}
	if isErr {
		t.Fatalf("超时应是业务提示而非 isError: %s", text)
	}
	if !strings.Contains(text, "等待超时") {
		t.Fatalf("应提示等待超时:\n%s", text)
	}
}

// maxWaitTimeout 上界：极大 timeout_seconds 被 clamp，不能变成不可回收的长轮询。
// 这里只验证 clamp 存在（用极短真实等待不现实），转而单测常量本身。
func TestMaxWaitTimeoutBounded(t *testing.T) {
	if maxWaitTimeout > 15*time.Minute {
		t.Fatalf("maxWaitTimeout 过大: %v", maxWaitTimeout)
	}
	if maxWaitTimeout < time.Minute {
		t.Fatalf("maxWaitTimeout 过小: %v", maxWaitTimeout)
	}
}

func TestRequirementCompliance_FixedKind(t *testing.T) {
	core := &fakeCore{createResp: &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, createCode: http.StatusOK}
	srv := New(core, "v-test")
	_, isErr := callTool(t, srv, "requirement_compliance", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7", "wait": false,
	})
	if isErr {
		t.Fatal("requirement_compliance 调用失败")
	}
	if core.createKind != "requirement_compliance" {
		t.Fatalf("kind 应固定 requirement_compliance，实际 %s", core.createKind)
	}
}

// ---------- list_tasks / search_code（v3.121.0） ----------

func TestListTasks(t *testing.T) {
	core := &fakeCore{tasks: &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{{
		TaskId: "t-9", Status: "done", Kind: "requirement_compliance",
		Platform: "gitea", Owner: "acme", Repo: "app", Number: "7",
	}}}}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "list_tasks", map[string]any{"status": "done", "limit": float64(5)})
	if isErr {
		t.Fatalf("list_tasks 报错: %s", text)
	}
	if !strings.Contains(text, "t-9") || !strings.Contains(text, "acme") {
		t.Fatalf("list_tasks 缺任务摘要:\n%s", text)
	}
	if core.tasksStatus != "done" || core.tasksLimit != 5 {
		t.Fatalf("参数未透传: status=%q limit=%d", core.tasksStatus, core.tasksLimit)
	}

	// 缺省：status 空 + limit 0（服务端缺省 20）
	if _, isErr = callTool(t, srv, "list_tasks", map[string]any{}); isErr {
		t.Fatal("空参应可用（全量清单）")
	}
	if core.tasksStatus != "" || core.tasksLimit != 0 {
		t.Fatalf("缺省参数不符: status=%q limit=%d", core.tasksStatus, core.tasksLimit)
	}

	// 非法 status：工具层可读错误（不落 400 裸码）
	text, isErr = callTool(t, srv, "list_tasks", map[string]any{"status": "nope"})
	if !isErr || !strings.Contains(text, "pending/running/done/failed/stale") {
		t.Fatalf("非法 status 应 isError: isErr=%v text=%s", isErr, text)
	}

	// 服务端非 200 → 工具错误
	core.tasksCode = 500
	_, isErr = callTool(t, srv, "list_tasks", map[string]any{})
	if !isErr {
		t.Fatal("非 200 应 isError")
	}
}

func TestRunReview_CreateErrorStatus(t *testing.T) {
	core := &fakeCore{createResp: nil, createCode: http.StatusInternalServerError}
	// createResp 为 nil 时 fake 返回默认成功——改用显式失败。
	core.createResp = &argus.CreateTaskResponse{}
	core.createCode = http.StatusInternalServerError
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "review_pr", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "wait": false,
	})
	if !isErr {
		t.Fatalf("500 创建应 isError: %s", text)
	}
}

// ---------- 阶段进度装饰（v3.127.0） ----------

// TestGetReport_StagesDecorated get_report 内联 stages 与 stage_current：
// 轮询单跳可见进展（此前要再调 get_task_stages）。
func TestGetReport_StagesDecorated(t *testing.T) {
	core := &fakeCopilot{stages: map[string]any{
		"stages": []map[string]any{
			{"stage": "R1", "done": true},
			{"stage": "R2", "done": true},
			{"stage": "R3", "done": false},
			{"stage": "R4", "done": false},
		},
	}, stagesOK: true}
	core.getResp = &argus.GetTaskResponse{TaskId: "t-9", Status: "running"}
	srv := New(core, "v-test")
	text, isErr := callTool(t, srv, "get_report", map[string]any{"task_id": "t-9"})
	if isErr {
		t.Fatalf("get_report 报错: %s", text)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(text), &view); err != nil {
		t.Fatal(err)
	}
	if _, ok := view["stages"]; !ok {
		t.Fatalf("应内联 stages:\n%s", text)
	}
	if view["stage_current"] != "R3" {
		t.Errorf("stage_current = %v, want R3", view["stage_current"])
	}

	// core 不实现 Copilot（纯 fakeCore）→ 无 stages 字段但输出如常。
	srv2 := New(&fakeCore{getResp: &argus.GetTaskResponse{TaskId: "t-1", Status: "running"}}, "v-test")
	text2, _ := callTool(t, srv2, "get_report", map[string]any{"task_id": "t-1"})
	var view2 map[string]any
	_ = json.Unmarshal([]byte(text2), &view2)
	if _, ok := view2["stages"]; ok {
		t.Error("无 Copilot 能力不应带 stages")
	}
}
