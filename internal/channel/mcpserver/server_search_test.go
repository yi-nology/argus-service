package mcpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// fakeSearcher 实现 CodeSearcher。
type fakeSearcher struct {
	fakeCore
	pattern, glob string
	ignoreCase    bool
	out           string
	err           error
}

func (f *fakeSearcher) SearchCode(_ context.Context, _, _, _, _, pattern, glob string, ignoreCase bool) (string, error) {
	f.pattern, f.glob, f.ignoreCase = pattern, glob, ignoreCase
	return f.out, f.err
}

func TestSearchCode(t *testing.T) {
	fs := &fakeSearcher{out: "<data>search_code 检索结果</data>"}
	srv := New(fs, "v-test")
	text, isErr := callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
		"pattern": "limitUnbounded", "glob": "*.go", "ignore_case": true,
	})
	if isErr || !strings.Contains(text, "检索结果") {
		t.Fatalf("search_code: isErr=%v text=%s", isErr, text)
	}
	if fs.pattern != "limitUnbounded" || fs.glob != "*.go" || !fs.ignoreCase {
		t.Fatalf("参数未透传: pattern=%q glob=%q ignoreCase=%v", fs.pattern, fs.glob, fs.ignoreCase)
	}

	// 数据面 error → 工具错误
	fs.err = errors.New("search_code 未启用")
	_, isErr = callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7", "pattern": "x",
	})
	if !isErr {
		t.Fatal("未启用应 isError")
	}

	// 缺 pattern
	_, isErr = callTool(t, srv, "search_code", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "7",
	})
	if !isErr {
		t.Fatal("缺 pattern 应 isError")
	}
}

// fakeDiffTaskCreator 实现 DiffTaskCreator。
type fakeDiffTaskCreator struct {
	fakeCore
	resp *argus.CreateTaskResponse
	code int
	diff string
}

func (f *fakeDiffTaskCreator) CreateDiffTask(_ context.Context, diff string) (*argus.CreateTaskResponse, int) {
	f.diff = diff
	if f.code != 0 {
		return nil, f.code
	}
	if f.resp != nil {
		return f.resp, http.StatusOK
	}
	return &argus.CreateTaskResponse{TaskId: "t-diff-1", Status: "pending"}, http.StatusOK
}

func TestReviewDiffAsync(t *testing.T) {
	dc := &fakeDiffTaskCreator{}
	srv := New(dc, "v-test")
	text, isErr := callTool(t, srv, "review_diff_async", map[string]any{"diff": "diff --git a/x b/x\n+bug\n"})
	if isErr || !strings.Contains(text, "t-diff-1") || !strings.Contains(text, "get_report") {
		t.Fatalf("review_diff_async: isErr=%v text=%s", isErr, text)
	}
	if dc.diff != "diff --git a/x b/x\n+bug\n" {
		t.Fatalf("diff 未透传: %q", dc.diff)
	}

	// 空 diff：工具层先拦
	_, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "   "})
	if !isErr {
		t.Fatal("空 diff 应 isError")
	}
	// 400：为空或超限（落库面 48KB）
	dc.code = http.StatusBadRequest
	text, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "x"})
	if !isErr || !strings.Contains(text, "48KB") {
		t.Fatalf("400 应转可读错误: isErr=%v text=%s", isErr, text)
	}
	// 503：模型未配置
	dc.code = http.StatusServiceUnavailable
	text, isErr = callTool(t, srv, "review_diff_async", map[string]any{"diff": "x"})
	if !isErr || !strings.Contains(text, "模型未配置") {
		t.Fatalf("503 应转可读错误: isErr=%v text=%s", isErr, text)
	}
}

// ---------- search_knowledge（v3.125.0） ----------

// fakeKnowledgeSearcher 实现 KnowledgeSearcher。
type fakeKnowledgeSearcher struct {
	fakeCore
	query string
	topK  int
	out   string
	err   error
}

func (f *fakeKnowledgeSearcher) SearchKnowledge(_ context.Context, query string, topK int) (string, error) {
	f.query, f.topK = query, topK
	return f.out, f.err
}

func TestSearchKnowledge_RegisteredAndCalled(t *testing.T) {
	core := &fakeKnowledgeSearcher{out: "search_knowledge（query=规范）命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_knowledge" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 KnowledgeSearcher 后应注册 search_knowledge")
	}
	text, isErr := callTool(t, srv, "search_knowledge", map[string]any{"query": "规范", "top_k": float64(3)})
	if isErr {
		t.Fatalf("search_knowledge 报错: %s", text)
	}
	if core.query != "规范" || core.topK != 3 {
		t.Errorf("入参透传错: query=%q topK=%d", core.query, core.topK)
	}
	if !strings.Contains(text, "命中 1 条") {
		t.Errorf("输出不符:\n%s", text)
	}
	// 缺必填 → isError；数据面 error → isError
	_, isErr = callTool(t, srv, "search_knowledge", map[string]any{})
	if !isErr {
		t.Error("缺 query 应 isError")
	}
	core2 := &fakeKnowledgeSearcher{err: errors.New("知识库未启用（未配置知识源）")}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "search_knowledge", map[string]any{"query": "x"})
	if !isErr || !strings.Contains(text, "知识库未启用") {
		t.Errorf("未启用应 isError 含文案: isErr=%v text=%s", isErr, text)
	}
}

// ---------- search_sem（v3.126.0） ----------

// fakeSemSearcher 实现 SemSearcher。
type fakeSemSearcher struct {
	fakeCore
	query string
	topK  int
	out   string
	err   error
}

func (f *fakeSemSearcher) SearchSem(_ context.Context, _, _, _, _, query string, topK int) (string, error) {
	f.query, f.topK = query, topK
	return f.out, f.err
}

func TestSearchSem_RegisteredAndCalled(t *testing.T) {
	core := &fakeSemSearcher{out: "search_sem（query=认证）命中 1 条"}
	srv := New(core, "v-test")
	found := false
	for _, n := range listToolNames(t, srv) {
		if n == "search_sem" {
			found = true
		}
	}
	if !found {
		t.Fatal("实现 SemSearcher 后应注册 search_sem")
	}
	text, isErr := callTool(t, srv, "search_sem", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1",
		"query": "认证", "top_k": float64(3),
	})
	if isErr {
		t.Fatalf("search_sem 报错: %s", text)
	}
	if core.query != "认证" || core.topK != 3 {
		t.Errorf("入参透传错: query=%q topK=%d", core.query, core.topK)
	}
	// 缺必填 → isError；未启用 → isError
	_, isErr = callTool(t, srv, "search_sem", map[string]any{"platform": "gitea"})
	if !isErr {
		t.Error("缺 query 应 isError")
	}
	core2 := &fakeSemSearcher{err: errors.New("search_sem 未启用（ARGUS_SEMSEARCH=0 或未配置 ARGUS_EMBEDDING_*）")}
	srv2 := New(core2, "v-test")
	text, isErr = callTool(t, srv2, "search_sem", map[string]any{
		"platform": "gitea", "owner": "acme", "repo": "app", "number": "1", "query": "x",
	})
	if !isErr || !strings.Contains(text, "search_sem 未启用") {
		t.Errorf("未启用应 isError 含文案: isErr=%v text=%s", isErr, text)
	}
}
