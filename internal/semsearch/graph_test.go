// graph_test.go —— 图面（v5.9.0 起）行为测试：建图与 stdlib 剪枝、路径与跳数
// 钳制、单符号上下文与同名多候选合并、命中附邻接、Manager 数据面
// （Trace/SymbolContext）；v5.10.0 增：NodeKey 键契约、跨包同名 import 限定
// 消歧与裸调用同目录优先、同接收者兄弟方法（跨文件）、qualified 目标缺失
// bare 回退、dot-import 回退、精确边替换词法（EdgeAugmenter 桩：成功/签名
// 门控/失败闩）与 SymbolContext 候选数。夹具取真实可编译形态（module +
// 多目录 + go.mod；go.mod 不在 codeExts 不进索引，仅作 module 声明供 import
// 剥前缀），embedding 复用 semsearch_test.go 的 stubEmbedder。
package semsearch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeFileSet 批量落盘测试夹具（目录自动创建）。
func writeFileSet(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// graphFixture 建图测试夹具（真实可编译形态：module + 同包 + 被引用子包）：
// Alpha→Beta→Gamma→Delta 调用链（a.go/b.go 根目录文件，键无 dir 前缀），Beta
// 另经 util.Gamma 跨包调用（import 剥模块前缀 qualified 消歧——与根包同名
// Gamma 是两个独立节点），Delta 调 fmt.Println（stdlib 剪枝面），z.go 孤立
// 符号（不可达面）。
func graphFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module fixture\n\ngo 1.22\n",
		"a.go": `package app

import "fixture/util"

func Alpha() {
	Beta()
}

func Beta() {
	Gamma()
	util.Gamma()
}
`,
		"b.go": `package app

import "fmt"

func Gamma() {
	Delta()
}

func Delta() {
	fmt.Println("done")
}
`,
		"util/util.go": "package util\n\nfunc Gamma() {}\n",
		"z.go":         "package app\n\nfunc Lonely() {}\n",
	})
	return dir
}

// assertChain 校验调用链的符号序与边向序（长度不符直接 Fatal）。
func assertChain(t *testing.T, got []SymRef, wantSymbols, wantRels []string) {
	t.Helper()
	if len(got) != len(wantSymbols) {
		t.Fatalf("链长 = %d, want %d: %+v", len(got), len(wantSymbols), got)
	}
	for i, s := range wantSymbols {
		if got[i].Symbol != s {
			t.Errorf("chain[%d].Symbol = %q, want %q", i, got[i].Symbol, s)
		}
		if got[i].Rel != wantRels[i] {
			t.Errorf("chain[%d].Rel = %q, want %q", i, got[i].Rel, wantRels[i])
		}
	}
}

// refSymbols 提取符号序列（邻接断言辅助）。
func refSymbols(refs []SymRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Symbol
	}
	return out
}

// TestNodeKey 节点键契约：dir 限定 / 根目录无前缀 / 接收者段。
func TestNodeKey(t *testing.T) {
	cases := []struct{ dir, recv, name, want string }{
		{"", "", "Alpha", "Alpha"},
		{"", "Store", "GetUser", "Store.GetUser"},
		{"internal/app", "", "New", "internal/app.New"},
		{"internal/app", "Store", "GetUser", "internal/app.Store.GetUser"},
	}
	for _, c := range cases {
		if got := NodeKey(c.dir, c.recv, c.name); got != c.want {
			t.Errorf("NodeKey(%q,%q,%q) = %q, want %q", c.dir, c.recv, c.name, got, c.want)
		}
	}
	if got := dirOf("a.go"); got != "" {
		t.Errorf("dirOf 根目录 = %q, want 空串", got)
	}
	if got := dirOf("internal/app/store.go"); got != "internal/app" {
		t.Errorf("dirOf = %q, want internal/app", got)
	}
}

// TestGraph_Build 建图：调用链同目录裸调用解析、跨包 import 限定消歧（同名
// Gamma 双节点互不污染）、stdlib 剪枝不出边、命中邻接可读。
func TestGraph_Build(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	chain, ok := ix.Path("Alpha", "Delta", graphHopsDefault)
	if !ok {
		t.Fatal("Alpha→Delta 应可达（3 跳）")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	// 定义位置随链输出；链上的 Gamma 是 b.go 的根包符号（同目录裸调用）。
	wantLoc := []struct {
		path string
		line int
	}{{"a.go", 5}, {"a.go", 9}, {"b.go", 5}, {"b.go", 9}}
	for i, w := range wantLoc {
		if chain[i].Path != w.path || chain[i].Line != w.line {
			t.Errorf("chain[%d] loc = %s:%d, want %s:%d", i, chain[i].Path, chain[i].Line, w.path, w.line)
		}
	}
	ix.mu.Lock()
	nDeltaCallees := len(ix.sym.adj["Delta"])
	deltaNeighbors := ix.sym.neighborsOf("Delta")
	betaOut := append([]string(nil), ix.sym.adj["Beta"]...)
	alphaNeighbors := ix.sym.neighborsOf("Alpha")
	ix.mu.Unlock()
	// 剪枝：Delta 调 fmt.Println 不入图——无仓库内被调，邻接只剩调用方 Gamma，
	// 不含 fmt.Println/Println。
	if nDeltaCallees != 0 {
		t.Errorf("Delta 被调应剪枝为空, got %d 项", nDeltaCallees)
	}
	for _, r := range deltaNeighbors {
		if r.Symbol == "fmt.Println" || r.Symbol == "Println" {
			t.Errorf("stdlib 剪枝失败: Delta 邻接含 %q", r.Symbol)
		}
	}
	if len(deltaNeighbors) != 1 || deltaNeighbors[0].Symbol != "Gamma" || deltaNeighbors[0].Rel != relCaller {
		t.Errorf("Delta 邻接 = %+v, want [{Gamma caller}]", deltaNeighbors)
	}
	// 消歧：Beta 的被调是根包 Gamma（同目录裸调用优先）+ util.Gamma（import
	// 限定 qualified）——两个同名独立节点，互不假边污染。
	if len(betaOut) != 2 || betaOut[0] != "Gamma" || betaOut[1] != "util.Gamma" {
		t.Errorf("Beta 被调 = %v, want [Gamma util.Gamma]（同目录优先 + import 限定）", betaOut)
	}
	if len(alphaNeighbors) != 1 || alphaNeighbors[0].Symbol != "Beta" || alphaNeighbors[0].Rel != relCallee {
		t.Errorf("Alpha 邻接 = %+v, want [{Beta callee}]", alphaNeighbors)
	}
}

// TestGraph_Path 路径查询：跳数不足不可达、超界钳制、无向 BFS 逆向边向
// called_by、同符号单步、孤立/未知符号 false。
func TestGraph_Path(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Path("Alpha", "Delta", 2); ok {
		t.Error("maxHops=2 不应到达（链需 3 跳）")
	}
	if _, ok := ix.Path("Alpha", "Delta", 1); ok {
		t.Error("maxHops=1 不应到达")
	}
	chain, ok := ix.Path("Alpha", "Delta", 99)
	if !ok {
		t.Fatal("maxHops 超界应钳制到 graphHopsMax 并找到")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	rev, ok := ix.Path("Delta", "Alpha", graphHopsMax)
	if !ok {
		t.Fatal("逆向应可达（无向 BFS）")
	}
	assertChain(t, rev,
		[]string{"Delta", "Gamma", "Beta", "Alpha"},
		[]string{"", relCalledBy, relCalledBy, relCalledBy})
	self, ok := ix.Path("Beta", "Beta", graphHopsDefault)
	if !ok || len(self) != 1 || self[0].Symbol != "Beta" || self[0].Path != "a.go" || self[0].Rel != "" {
		t.Errorf("同符号应单步返回 self: %+v ok=%v", self, ok)
	}
	if _, ok := ix.Path("Alpha", "Lonely", graphHopsMax); ok {
		t.Error("孤立符号不可达")
	}
	if _, ok := ix.Path("Alpha", "NoSuchSymbol", graphHopsMax); ok {
		t.Error("未知符号应 false")
	}
}

// TestGraph_SameNameDisambiguation 跨包同名消歧（v5.10.0）：两包各一个 New，
// 包级裸调用同目录优先（另一包同名不混入），import 限定精确指向目标包节点。
func TestGraph_SameNameDisambiguation(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module disamb\n\ngo 1.22\n",
		"a/a.go": `package a

import "disamb/b"

func New() {}

func UseLocal() {
	New()
}

func UseCross() {
	b.New()
}
`,
		"b/b.go": "package b\n\nfunc New() {}\n",
	})
	if got := modulePathOf(dir); got != "disamb" {
		t.Errorf("modulePathOf = %q, want disamb", got)
	}
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	localOut := append([]string(nil), ix.sym.adj["a.UseLocal"]...)
	crossOut := append([]string(nil), ix.sym.adj["a.UseCross"]...)
	localIn := append([]string(nil), ix.sym.radj["a.New"]...)
	crossIn := append([]string(nil), ix.sym.radj["b.New"]...)
	ix.mu.Unlock()
	if len(localOut) != 1 || localOut[0] != "a.New" {
		t.Errorf("UseLocal 被调 = %v, want [a.New]（裸调用同目录优先，b.New 不混入）", localOut)
	}
	if len(crossOut) != 1 || crossOut[0] != "b.New" {
		t.Errorf("UseCross 被调 = %v, want [b.New]（import 限定消歧）", crossOut)
	}
	if len(localIn) != 1 || localIn[0] != "a.UseLocal" {
		t.Errorf("a.New 调用方 = %v, want [a.UseLocal]", localIn)
	}
	if len(crossIn) != 1 || crossIn[0] != "a.UseCross" {
		t.Errorf("b.New 调用方 = %v, want [a.UseCross]（同名节点无假边污染）", crossIn)
	}
}
