package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// ---------- 测试替身（消费侧窄接口 Core/ReviewQA/Copilot 的最小实现） ----------

// fakeCore 实现 Core；可选实现 ReviewQA/Copilot 由嵌入与否决定。
type fakeCore struct {
	createResp  *argus.CreateTaskResponse
	createCode  int
	createCalls int
	createKind  string

	// getStatus 队列：每次 GetTask 弹出一态；耗尽后粘住最后一态。
	statuses []string
	getCalls int
	getCode  int
	getResp  *argus.GetTaskResponse

	agents *argus.ListAgentsResponse

	tasks       *argus.ListTasksResponse
	tasksCode   int
	tasksStatus string
	tasksLimit  int
}

func (f *fakeCore) CreateTaskDirect(_ context.Context, kind, _, _, _, _ string, _ map[string]string) (*argus.CreateTaskResponse, int) {
	f.createCalls++
	f.createKind = kind
	if f.createResp == nil {
		return &argus.CreateTaskResponse{TaskId: "t-1", Status: "queued"}, http.StatusOK
	}
	return f.createResp, f.createCode
}

func (f *fakeCore) GetTask(_ context.Context, _ string) (*argus.GetTaskResponse, int) {
	f.getCalls++
	code := f.getCode
	if code == 0 {
		code = http.StatusOK
	}
	if code != http.StatusOK {
		return nil, code
	}
	if f.getResp != nil {
		return f.getResp, code
	}
	if len(f.statuses) == 0 {
		return &argus.GetTaskResponse{TaskId: "t-1", Status: "queued"}, code
	}
	st := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}
	return &argus.GetTaskResponse{TaskId: "t-1", Status: st}, code
}

func (f *fakeCore) ListAgents(_ context.Context, _, _, _ string) *argus.ListAgentsResponse {
	if f.agents != nil {
		return f.agents
	}
	return &argus.ListAgentsResponse{Agents: []*argus.AgentManifestDTO{{Name: "correctness"}}}
}

func (f *fakeCore) ListTasks(_ context.Context, status string, limit int) (*argus.ListTasksResponse, int) {
	f.tasksStatus, f.tasksLimit = status, limit
	if f.tasksCode != 0 {
		return nil, f.tasksCode
	}
	if f.tasks != nil {
		return f.tasks, http.StatusOK
	}
	return &argus.ListTasksResponse{Tasks: []*argus.TaskSummary{}}, http.StatusOK
}

// fakeQA 实现 ReviewQA。
type fakeQA struct {
	fakeCore
	latest   map[string]any
	latestOK bool
	answer   string
	answerOK bool
	askedQ   string
	askedNum string
}

func (f *fakeQA) LatestReview(_ context.Context, _, _, _, _ string) (map[string]any, bool) {
	return f.latest, f.latestOK
}

func (f *fakeQA) AnswerAboutReview(_ context.Context, _, _, _, number, question string) (string, bool) {
	f.askedQ = question
	f.askedNum = number
	return f.answer, f.answerOK
}

// fakeCopilot 实现 Copilot。
type fakeCopilot struct {
	fakeQA
	stages     map[string]any
	stagesOK   bool
	findings   []map[string]any
	findingsOK bool
	suggestion map[string]any
	suggOK     bool
	suggFP     string
}

func (f *fakeCopilot) TaskStages(_ context.Context, _ string) (map[string]any, bool) {
	return f.stages, f.stagesOK
}

func (f *fakeCopilot) Findings(_ context.Context, _ string) ([]map[string]any, bool) {
	return f.findings, f.findingsOK
}

func (f *fakeCopilot) Suggestion(_ context.Context, _ string, fp string) (map[string]any, bool) {
	f.suggFP = fp
	return f.suggestion, f.suggOK
}

// ---------- 调用助手 ----------

// callTool 走 JSON-RPC tools/call 全路径（注册校验 + 参数绑定 + handler），
// 返回 (文本内容, isError)。
func callTool(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
}, name string, args map[string]any) (string, bool) {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp := srv.HandleMessage(context.Background(), raw)
	if resp == nil {
		t.Fatal("HandleMessage 返回 nil")
	}
	// 经 JSON 往返取稳定形态（Result 可能是 *CallToolResult 或具体值）。
	rb, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal resp: %v", err)
	}
	var envelope struct {
		Error  *json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rb, &envelope); err != nil {
		t.Fatalf("unmarshal resp: %v\n%s", err, rb)
	}
	if envelope.Error != nil {
		t.Fatalf("JSON-RPC error: %s", *envelope.Error)
	}
	if len(envelope.Result.Content) == 0 {
		t.Fatalf("工具 %s 无内容: %s", name, rb)
	}
	return envelope.Result.Content[0].Text, envelope.Result.IsError
}

func listToolNames(t *testing.T, srv interface {
	HandleMessage(context.Context, json.RawMessage) mcp.JSONRPCMessage
}) []string {
	t.Helper()
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	resp := srv.HandleMessage(context.Background(), raw)
	rb, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rb, &envelope); err != nil {
		t.Fatalf("tools/list 解析失败: %v\n%s", err, rb)
	}
	names := make([]string, 0, len(envelope.Result.Tools))
	for _, tl := range envelope.Result.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func strPtr(s string) *string { return &s }

// ---------- 工具注册面 ----------

// 无 Copilot 时只注册核心五工具；副驾三工具不得出现（避免 tools/list 死工具）。
func TestNew_ToolRegistrationCoreOnly(t *testing.T) {
	srv := New(&fakeCore{}, "v-test")
	names := listToolNames(t, srv)
	want := map[string]bool{
		"review_pr": true, "requirement_compliance": true,
		"list_agents": true, "get_report": true, "ask_argus": true, "list_tasks": true,
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("核心模式多出工具 %s", n)
		}
		delete(want, n)
	}
	for missing := range want {
		t.Errorf("核心模式缺工具 %s", missing)
	}
	for _, banned := range []string{"get_task_stages", "list_findings", "get_suggestion", "search_code", "search_knowledge", "search_sem", "review_diff_async", "fix_finding"} {
		for _, n := range names {
			if n == banned {
				t.Errorf("core 未实现 Copilot 却注册了 %s", banned)
			}
		}
	}
}

// 实现 Copilot 后副驾三工具挂载。
func TestNew_ToolRegistrationWithCopilot(t *testing.T) {
	srv := New(&fakeCopilot{}, "v-test")
	names := listToolNames(t, srv)
	for _, want := range []string{
		"review_pr", "requirement_compliance", "list_agents", "get_report", "ask_argus", "list_tasks",
		"get_task_stages", "list_findings", "get_suggestion",
	} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("缺工具 %s（注册面: %v）", want, names)
		}
	}
}

// fakeRepoCfgCore 在 fakeCore 之上实现按仓配置视图面（get_repo_config 数据面）。
type fakeRepoCfgCore struct {
	fakeCore
	view map[string]any
}

func (f *fakeRepoCfgCore) RepoSettingsView(_ context.Context, _, _, _ string) (map[string]any, int) {
	return f.view, http.StatusOK
}

// core 实现 RepoConfigViewer 时注册 get_repo_config 并可调用（v3.187.0）；
// 未实现时 tools/list 不出现死工具（TestNew_ToolRegistrationCoreOnly 已覆盖反向）。
func TestNew_RepoConfigTool(t *testing.T) {
	srv := New(&fakeRepoCfgCore{view: map[string]any{
		"found": true, "source": "repo_override",
		"settings":  map[string]any{"max_diff_chars": 200000},
		"effective": map[string]any{"monthly_token_budget": 5000000, "task_token_budget": 400000, "max_diff_chars": 200000},
	}}, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "get_repo_config" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 RepoConfigViewer 后应注册 get_repo_config")
	}
	text, isErr := callTool(t, srv, "get_repo_config",
		map[string]any{"platform": "gitea", "owner": "acme", "repo": "web"})
	if isErr {
		t.Fatalf("应成功: %s", text)
	}
	if !strings.Contains(text, "repo_override") || !strings.Contains(text, "max_diff_chars") {
		t.Fatalf("视图内容缺失: %s", text)
	}
	// 缺必填 → 工具级错误回执（非 panic）。
	if _, isErr := callTool(t, srv, "get_repo_config", map[string]any{"platform": "gitea"}); !isErr {
		t.Fatal("缺 owner/repo 应 isError")
	}
}
