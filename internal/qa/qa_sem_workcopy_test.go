package qa

// 语义检索成功路径（本地 file:// git fixture，零网络）：topK 钳制必须有可观测
// 效果（0→缺省 5、超上限→10），并覆盖 get_trace/get_symbol_context 的成功渲染。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oagentkit/tools/workcopy"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/semsearch"
)

// makeSemFixtureRepo 本地 git fixture：main 上提交一次并建 refs/pull/7/head，
// file:// 克隆零网络（与 adapter_cli 的 fixture 同款）。
func makeSemFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-b", "main", dir)

	var src strings.Builder
	src.WriteString("package app\n\n")
	// 12 个含 db 的顶层函数：query=db 的命中数足以区分缺省 5 与上限 10。
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&src, "// F%d 查询 db。\nfunc F%d() {}\n\n", i, i)
	}
	// 三跳调用链 + 邻接：Alpha → Beta → Gamma。
	src.WriteString("func Alpha() { Beta() }\n\nfunc Beta() { Gamma() }\n\nfunc Gamma() {}\n")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte(src.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "init")
	runGit("update-ref", "refs/pull/7/head", "main")
	return dir
}

// newSemWorkcopyEngine 语义引擎 + 可克隆的本地工作副本（Ensure 成功）。
func newSemWorkcopyEngine(t *testing.T) *Engine {
	t.Helper()
	e, _, _ := newTestEngine(t, &fakeModel{})
	fixture := makeSemFixtureRepo(t)
	e.SemSearch = semsearch.NewManager(qaStubEmbedder{})
	e.SemSearchOn = true
	e.WC = workcopy.NewPool(t.TempDir(), logx.Nop())
	e.WC.CredentialOf = func(string) (string, string, bool) { return "file://" + fixture, "x", true }
	return e
}

func TestSearchSemTopKClampObservable(t *testing.T) {
	e := newSemWorkcopyEngine(t)
	ctx := context.Background()

	// topK<=0 → 缺省 5：命中数上限可观测。
	out, err := e.SearchSem(ctx, "gitea", "acme", "app", "7", "db", 0)
	if err != nil {
		t.Fatalf("SearchSem(topK=0): %v", err)
	}
	if !strings.Contains(out, "命中 5 条") {
		t.Fatalf("topK=0 应钳制为缺省 5: %s", firstLine(out))
	}
	// topK 超上限 → 10。
	out, err = e.SearchSem(ctx, "gitea", "acme", "app", "7", "db", 999)
	if err != nil {
		t.Fatalf("SearchSem(topK=999): %v", err)
	}
	if !strings.Contains(out, "命中 10 条") {
		t.Fatalf("topK=999 应钳制为上限 10: %s", firstLine(out))
	}
}

func TestTraceAndSymbolContextSuccess(t *testing.T) {
	e := newSemWorkcopyEngine(t)
	ctx := context.Background()

	out, err := e.Trace(ctx, "gitea", "acme", "app", "7", "Alpha", "Gamma")
	if err != nil {
		t.Fatalf("Trace: %v", err)
	}
	if !strings.Contains(out, "—calls→") || !strings.Contains(out, "get_trace 调用链（数据）") {
		t.Fatalf("调用链渲染错: %s", firstLine(out))
	}

	out, err = e.Trace(ctx, "gitea", "acme", "app", "7", "Alpha", "NoSuchSymbol")
	if err != nil {
		t.Fatalf("不可达 Trace 不应报错: %v", err)
	}
	if !strings.Contains(out, "未找到") {
		t.Fatalf("不可达应返回提示文本: %s", firstLine(out))
	}

	out, err = e.SymContext(ctx, "gitea", "acme", "app", "7", "Beta")
	if err != nil {
		t.Fatalf("SymContext: %v", err)
	}
	if !strings.Contains(out, "定义：Beta") || !strings.Contains(out, "调用方") ||
		!strings.Contains(out, "get_symbol_context 图上下文（数据）") {
		t.Fatalf("符号上下文渲染错: %s", firstLine(out))
	}

	out, err = e.SymContext(ctx, "gitea", "acme", "app", "7", "NoSuchSymbol")
	if err != nil {
		t.Fatalf("未知符号不应报错: %v", err)
	}
	if !strings.Contains(out, "未在索引内解析到符号") {
		t.Fatalf("未知符号应返回提示文本: %s", firstLine(out))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
