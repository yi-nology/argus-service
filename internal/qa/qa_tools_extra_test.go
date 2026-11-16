package qa

// Q&A 工具执行面测试：get_diff/get_file 闭包、search_similar_issues 能力路由、
// search_code 的惰性/绑定目录/不可用三形态——全部本地假 SDK/临时目录。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
)

func invokeTool(t *testing.T, tl tool.BaseTool, args string) string {
	t.Helper()
	it, ok := tl.(tool.InvokableTool)
	if !ok {
		t.Fatalf("%T 不是 InvokableTool", tl)
	}
	out, err := it.InvokableRun(context.Background(), args)
	if err != nil {
		t.Fatalf("工具执行失败: %v", err)
	}
	return out
}

func TestDiffFileToolsExecution(t *testing.T) {
	m := &fakeModel{}
	e, _, fp := newTestEngine(t, m)
	getDiff, getFile, err := e.diffFileTools(context.Background(), "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}

	out := invokeTool(t, getDiff, `{}`)
	if !strings.Contains(out, "diff --git") || !strings.Contains(out, "get_diff（数据）") {
		t.Fatalf("get_diff 输出错: %q", out)
	}

	out = invokeTool(t, getFile, `{"path":"main.go"}`)
	if !strings.Contains(out, "1|package main") || !strings.Contains(out, "get_file 文件内容（数据）") {
		t.Fatalf("get_file 应带行号+围栏: %q", out)
	}

	// 文件不存在：错误回注提醒信封（不中断 ReAct）。
	out = invokeTool(t, getFile, `{"path":"absent.go"}`)
	if !strings.Contains(out, "error") {
		t.Fatalf("缺失文件应回注错误: %q", out)
	}

	// 超长内容截断（20k rune 上限）。
	fp.files["big.go"] = strings.Repeat("x", 25_000)
	out = invokeTool(t, getFile, `{"path":"big.go"}`)
	if !strings.Contains(out, "截断") {
		t.Fatalf("超长文件应截断: %q", out[len(out)-40:])
	}

	// 超长 diff 截断（30k 上限）。
	fp.diff.RawDiff = strings.Repeat("d", 31_000)
	out = invokeTool(t, getDiff, `{}`)
	if !strings.Contains(out, "截断") {
		t.Fatal("超长 diff 应截断")
	}
}

// searchFake fakePlatform + SearchManager 能力（可选能力断言路径）。
type searchFake struct {
	*fakePlatform
	provider.SearchManager
	results []*provider.SearchIssueResult
	err     error
	got     provider.SearchIssuesOptions
}

func (s *searchFake) SearchIssues(_ context.Context, opts provider.SearchIssuesOptions) ([]*provider.SearchIssueResult, *int, error) {
	s.got = opts
	return s.results, nil, s.err
}

func TestSimilarIssueToolCapabilityRouting(t *testing.T) {
	m := &fakeModel{}
	e, _, fp := newTestEngine(t, m)

	// 平台不实现 SearchManager：工具不加入（nil, nil）。
	if tl, err := e.similarIssueTool("gitea", "acme", "app"); err != nil || tl != nil {
		t.Fatalf("无能力平台应返回 nil,nil: %v %v", tl, err)
	}

	sf := &searchFake{fakePlatform: fp, results: []*provider.SearchIssueResult{
		{Number: "12", Title: "同名登录问题", State: provider.IssueStateOpen,
			WebURL: "http://x/12", Labels: []string{"bug"}},
		nil, // nil 元素必须跳过（防御）
	}}
	e.SDK = argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{"gitea": sf})
	tl, err := e.similarIssueTool("gitea", "acme", "app")
	if err != nil || tl == nil {
		t.Fatalf("similarIssueTool: %v %v", tl, err)
	}
	out := invokeTool(t, tl, `{"keywords":"auth"}`)
	var parsed similarOut
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("输出非 JSON: %v (%q)", err, out)
	}
	if len(parsed.Issues) != 1 || parsed.Issues[0].Number != "12" ||
		parsed.Issues[0].Title != "同名登录问题" || parsed.Issues[0].State != "open" {
		t.Fatalf("issues = %+v", parsed.Issues)
	}
	if sf.got.Query != "auth" || sf.got.Repo != "acme/app" || sf.got.PerPage != 10 {
		t.Fatalf("检索参数错: %+v", sf.got)
	}

	// 平台检索失败：错误回注，不当作工具错误。
	sf.err = errors.New("search 500")
	out = invokeTool(t, tl, `{"keywords":"auth"}`)
	parsed = similarOut{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Error == "" || len(parsed.Issues) != 0 {
		t.Fatalf("检索失败应回注 error: %+v", parsed)
	}
}

func TestSearchCodeToolFetchVariants(t *testing.T) {
	m := &fakeModel{}
	e, _, _ := newTestEngine(t, m)

	// fetch 空 = 工作副本不可用：降级文案而非工具错误。
	unavailable := e.newSearchCodeTool(func(context.Context) string { return "" })
	out := invokeTool(t, unavailable, `{"pattern":"x"}`)
	if !strings.Contains(out, "工作副本不可用") {
		t.Fatalf("不可用应给降级文案: %q", out)
	}

	// 真实目录：执行 codesearch 并包数据围栏。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n// needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bound := e.newSearchCodeTool(func(context.Context) string { return dir })
	out = invokeTool(t, bound, `{"pattern":"needle"}`)
	if !strings.Contains(out, "search_code 检索结果") || !strings.Contains(out, "a.go") {
		t.Fatalf("检索输出错: %q", out)
	}

	// 惰性形态：首调解析目录（池无凭证 → 解析失败 → 降级文案）。
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	lazy := e.searchCodeTool("gitea", "acme", "app", "7")
	out = invokeTool(t, lazy, `{"pattern":"needle"}`)
	if !strings.Contains(out, "工作副本不可用") {
		t.Fatalf("惰性解析失败应降级: %q", out)
	}
}

func TestSearchCodeGuardsAndWorkcopyFailure(t *testing.T) {
	m := &fakeModel{}
	e, _, _ := newTestEngine(t, m)
	ctx := context.Background()

	if _, err := e.SearchCode(ctx, "gitea", "acme", "app", "7", "x", "", false); err == nil ||
		!strings.Contains(err.Error(), "未启用") {
		t.Fatalf("WC nil 应未启用: %v", err)
	}

	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	e.CodeSearch = false
	if _, err := e.SearchCode(ctx, "gitea", "acme", "app", "7", "x", "", false); err == nil ||
		!strings.Contains(err.Error(), "未启用") {
		t.Fatalf("CodeSearch=false 应未启用: %v", err)
	}

	e.CodeSearch = true
	if _, err := e.SearchCode(ctx, "gitea", "acme", "app", "7", "x", "", false); err == nil ||
		!strings.Contains(err.Error(), "工作副本不可用") {
		t.Fatalf("池无凭证应报工作副本不可用: %v", err)
	}

	if _, ok := e.workcopyDir(ctx, "gitea", "acme", "app", "7"); ok {
		t.Fatal("池无凭证时 workcopyDir 应 false")
	}
}

func TestBuildToolsIncludesCoreAndOptional(t *testing.T) {
	m := &fakeModel{}
	e, _, fp := newTestEngine(t, m)
	ctx := context.Background()

	tools, strategy, err := e.tools(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	// 无 WC/无 SearchManager/无 Knowledge：核心 get_diff + get_file 两枚。
	if len(tools) != 2 {
		t.Fatalf("工具数 = %d, want 2（核心两枚）", len(tools))
	}
	if strategy == "" {
		t.Fatal("工具策略提示不应为空")
	}

	// 挂上 search_code（WC + 开关）与 similar issues（SearchManager）。
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	e.CodeSearch = true
	e.SDK = argusctx.NewSDKCtxStubbed(testNopLogger{}, map[string]provider.Provider{
		"gitea": &searchFake{fakePlatform: fp},
	})
	tools, _, err = e.tools(ctx, "gitea", "acme", "app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 {
		t.Fatalf("工具数 = %d, want 4（核心+search_code+similar_issues）", len(tools))
	}
}
