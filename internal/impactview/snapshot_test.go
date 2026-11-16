// snapshot_test.go —— 影响面管线单测：fixture 临时仓覆盖 L0/L1/L2/L3/共变/
// 风险分/渲染三形态/确定性。git 共变用例依赖本机 git 命令（CI 同 argus 运行
// 环境，恒有）。
package impactview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oagentkit/textx/codeintel"
)

// writeFixture 生成一个临时 Go 模块：
//
//	pkga/a.go        Func + method（改动目标）
//	pkga/a_test.go   TestFunc（改动包自带测试）
//	pkgb/b.go        import pkga，调 Func + method（直接依赖方，带测试）
//	pkgc/c.go        import pkgb（二跳依赖方）
//	pkgd/d.go        不相关包
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n\ngo 1.22\n",
		"pkga/a.go": `package pkga

// Func 被测函数。
func Func(x int) int { return x + 1 }

// Item 被测类型。
type Item struct{ N int }

// Grow 被测方法。
func (i Item) Grow() Item { i.N++; return i }
`,
		"pkga/a_test.go": `package pkga

import "testing"

func TestFunc(t *testing.T) { _ = Func(1) }
`,
		"pkgb/b.go": `package pkgb

import "example.com/fix/pkga"

// Wrap 调用方。
func Wrap(x int) int { return pkga.Func(x) }

// Bundle 方法调用方。
func Bundle() int { i := pkga.Item{N: 1}; return i.Grow().N }
`,
		"pkgb/b_test.go": `package pkgb

import "testing"

func TestWrap(t *testing.T) { _ = Wrap(1) }
`,
		"pkgc/c.go": `package pkgc

import "example.com/fix/pkgb"

// Deep 二跳调用。
func Deep() int { return pkgb.Wrap(2) }
`,
		"pkgd/d.go": `package pkgd

// Unrelated 无关。
func Unrelated() {}
`,
	}
	for rel, body := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// gitCmd 在临时仓跑 git（测试初始化用）。
func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func changedOf(paths ...string) map[string][][2]int {
	m := map[string][][2]int{}
	for _, p := range paths {
		m[p] = [][2]int{{1, 20}} // 整文件视作触达
	}
	return m
}

// TestBuildLayers 分层管线端到端：L0 文件 → L1 闭包 → L2 调用方 → L3 测试。
func TestBuildLayers(t *testing.T) {
	dir := writeFixture(t)
	idx, err := codeintel.NewIndex(dir, 2000)
	if err != nil {
		t.Fatalf("建索引: %v", err)
	}
	s := Build(context.Background(), Input{
		Dir:          dir,
		ChangedFiles: []string{"pkga/a.go"},
		HunkRanges:   changedOf("pkga/a.go"),
		DiffText:     "+func Func(x, y int) int",
		Index:        idx,
		MaxCommits:   -1, // 共变另行用例
	})
	if s.Version != SnapshotVersion {
		t.Fatalf("version = %d", s.Version)
	}
	if len(s.Changed) != 1 || s.Changed[0] != "pkga/a.go" {
		t.Fatalf("changed = %v", s.Changed)
	}
	// L1：pkgb（一跳）与 pkgc（二跳）必须入闭包；pkgd 不入
	pkgPaths := map[string]PkgImpact{}
	for _, p := range s.Packages {
		pkgPaths[p.Path] = p
	}
	if _, ok := pkgPaths["pkgb"]; !ok {
		t.Fatalf("pkgb 未入闭包: %+v", s.Packages)
	}
	if _, ok := pkgPaths["pkgc"]; !ok {
		t.Fatalf("pkgc 未入闭包: %+v", s.Packages)
	}
	if _, ok := pkgPaths["pkgd"]; ok {
		t.Fatal("pkgd 不应入闭包")
	}
	if pkgPaths["pkgb"].Depth != 1 || pkgPaths["pkgc"].Depth != 2 {
		t.Fatalf("depth 错: %+v %+v", pkgPaths["pkgb"], pkgPaths["pkgc"])
	}
	if pkgPaths["pkgb"].Tests != 1 {
		t.Fatalf("pkgb 测试数 = %d", pkgPaths["pkgb"].Tests)
	}
	// L3：pkgb/pkga 测试节点存在
	if !s.hasNode("test:pkgb") || !s.hasNode("test:pkga") {
		t.Fatalf("测试节点缺失: %+v", nodeIDs(s))
	}
	// L2：Func 的调用方 pkgb/b.go 必须出现（AST 精化后带封闭函数）
	foundWrap := false
	for _, n := range s.Nodes {
		if n.Kind == KindFile && n.ID == "file:pkgb/b.go" {
			foundWrap = true
		}
	}
	if !foundWrap {
		t.Fatalf("调用方 pkgb/b.go 缺失: %+v", nodeIDs(s))
	}
	// 边方向契约：符号 → 调用方
	hasCallerEdge := false
	for _, e := range s.Edges {
		if e.Rel == RelCaller && strings.HasPrefix(e.To, "file:pkgb/b.go") {
			hasCallerEdge = true
		}
	}
	if !hasCallerEdge {
		t.Fatalf("caller 边缺失: %+v", s.Edges)
	}
	// 风险：导出符号 + 签名变更 → 至少 medium
	if s.Risk.Score < 25 {
		t.Fatalf("风险分过低: %+v", s.Risk)
	}
	sigFound := false
	for _, r := range s.Risk.Reasons {
		if strings.Contains(r, "签名") {
			sigFound = true
		}
	}
	if !sigFound {
		t.Fatalf("签名变更理由缺失: %+v", s.Risk.Reasons)
	}
}

// TestBuildNoWorkspace 无工作区降级：仅 L0 + 注明。
func TestBuildNoWorkspace(t *testing.T) {
	s := Build(context.Background(), Input{ChangedFiles: []string{"x/y.go"}})
	if len(s.Nodes) != 1 {
		t.Fatalf("nodes = %+v", s.Nodes)
	}
	if len(s.Notes) == 0 {
		t.Fatal("应有降级注明")
	}
}

// TestBuildEmpty 空 changed：空快照 + 注明。
func TestBuildEmpty(t *testing.T) {
	s := Build(context.Background(), Input{})
	if len(s.Nodes) != 0 || len(s.Notes) != 1 {
		t.Fatalf("nodes=%d notes=%v", len(s.Nodes), s.Notes)
	}
}

// TestCoupling 共变：3 次 commit 让 other 与 changed 同改 → 入榜；单次共现不入。
func TestCoupling(t *testing.T) {
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "config", "user.email", "t@t")
	gitCmd(t, dir, "config", "user.name", "t")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n\nfunc main() {}\n")
	write("partner.go", "package main\n\nvar A = 1\n")
	write("once.go", "package main\n\nvar B = 1\n")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", "init")
	for i := range 3 {
		write("main.go", "package main\n\nfunc main() { _ = "+string(rune('a'+i))+" }\n")
		write("partner.go", "package main\n\nvar A = "+itoaN(i+2)+"\n")
		gitCmd(t, dir, "commit", "-am", "bump")
	}
	couples, commits, note := collectCoupling(context.Background(), dir, []string{"main.go"}, 10, 10*time.Second)
	if note != "" {
		t.Fatalf("note = %s", note)
	}
	if commits < 4 {
		t.Fatalf("commits = %d", commits)
	}
	foundPartner, foundOnce := false, false
	for _, c := range couples {
		if c.Other == "partner.go" && c.Count >= MinCoupleCount {
			foundPartner = true
		}
		if c.Other == "once.go" {
			foundOnce = true
		}
	}
	if !foundPartner {
		t.Fatalf("强共变 partner.go 未入榜: %+v", couples)
	}
	if foundOnce {
		t.Fatal("单次共现 once.go 不应入榜")
	}
}

func itoaN(n int) string { return string(rune('0' + n)) }
