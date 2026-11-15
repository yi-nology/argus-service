package semsearch

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// stubAugmenter EdgeAugmenter 桩：可注入精确边或错误（失败闩面）。
type stubAugmenter struct {
	edges [][2]string
	err   error
	calls int
}

func (a *stubAugmenter) Edges(_ context.Context, _ string) ([][2]string, error) {
	a.calls++
	if a.err != nil {
		return nil, a.err
	}
	return a.edges, nil
}

// TestPreciseEdgesReplaceLexical 精确边替换词法（v5.10.0）：成功应用后邻接
// 全量来自精确边（词法边消失）、符号表照建、签名门控（未变不重拉、变更才重拉）。
func TestPreciseEdgesReplaceLexical(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module pz\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc Caller() {\n\thelper()\n}\n\nfunc helper() {}\n",
		"b/b.go": "package b\n\nfunc New() {}\n",
	})
	// 精确边含一条词法图不可能有的跨包边（a.Caller→b.New：裸调用不可跨包）。
	aug := &stubAugmenter{edges: [][2]string{{"a.Caller", "b.New"}}}
	ix := NewIndex(&stubEmbedder{})
	ix.aug = aug
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	callerOut := append([]string(nil), ix.sym.adj["a.Caller"]...)
	helperIn := append([]string(nil), ix.sym.radj["a.helper"]...)
	newIn := append([]string(nil), ix.sym.radj["b.New"]...)
	precise := ix.precise
	ix.mu.Unlock()
	if !precise {
		t.Error("精确边应用后应置位 precise")
	}
	if len(callerOut) != 1 || callerOut[0] != "b.New" {
		t.Errorf("精确模式 a.Caller 被调 = %v, want [b.New]（精确边替换词法）", callerOut)
	}
	if len(helperIn) != 0 {
		t.Errorf("词法边应被替换: a.helper 调用方 = %v, want 空", helperIn)
	}
	if len(newIn) != 1 || newIn[0] != "a.Caller" {
		t.Errorf("b.New 调用方 = %v, want [a.Caller]", newIn)
	}
	// 符号表照建（lex 面）：精确模式下定义与候选照常可查。
	if self, _, _, cands, ok := ix.SymbolContext("a.helper"); !ok || cands != 1 || self.Path != "a/a.go" {
		t.Errorf("精确模式下符号表照建: self=%+v cands=%d ok=%v", self, cands, ok)
	}
	// 签名门控：文件集未变不重拉。
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 1 {
		t.Errorf("签名未变不应再调 augmenter: calls=%d", aug.calls)
	}
	// 文件变更（size 变）→ 签名变 → 重拉精确边。
	writeFileSet(t, dir, map[string]string{
		"a/a.go": "package a\n\nfunc Caller() {\n\thelper()\n}\n\nfunc helper() {}\n\nfunc Extra() {}\n",
	})
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 2 {
		t.Errorf("签名变更应再调 augmenter: calls=%d", aug.calls)
	}
	if _, _, _, _, ok := ix.SymbolContext("a.Extra"); !ok {
		t.Error("变更后新符号应入表")
	}
}

// TestPreciseEdgesFailureLatch 精确边失败闩（v5.10.0）：augmenter 失败降级词法
// （邻接保持、precise 不置位），本 Index 生命周期内不再重试。
func TestPreciseEdgesFailureLatch(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module latch\n\ngo 1.22\n",
		"a.go":   "package app\n\nfunc Alpha() {\n\tBeta()\n}\n\nfunc Beta() {}\n",
	})
	aug := &stubAugmenter{err: errors.New("工作副本编译失败")}
	ix := NewIndex(&stubEmbedder{})
	ix.aug = aug
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	ix.mu.Lock()
	alphaOut := append([]string(nil), ix.sym.adj["Alpha"]...)
	precise := ix.precise
	latched := ix.augFailed
	ix.mu.Unlock()
	if len(alphaOut) != 1 || alphaOut[0] != "Beta" {
		t.Errorf("augmenter 失败应保持词法邻接, got %v", alphaOut)
	}
	if precise || !latched {
		t.Errorf("失败应置失败闩且不置 precise: precise=%v latched=%v", precise, latched)
	}
	// 失败闩：第二次 Refresh（文件变更与否）不再调 augmenter。
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if aug.calls != 1 {
		t.Errorf("失败闩后不应重试: calls=%d", aug.calls)
	}
}

// TestManagerWithEdges Manager 数据面接线（v5.10.0）：WithEdges 注入后
// Precise 透出精确模式，邻接经 Search/SymbolContext 可读；未配置/失败保持
// 词法（Precise=false）。
func TestManagerWithEdges(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod": "module mp\n\ngo 1.22\n",
		"a/a.go": "package a\n\nfunc Caller() {}\n",
		"b/b.go": "package b\n\nfunc Target() {}\n",
	})
	aug := &stubAugmenter{edges: [][2]string{{"a.Caller", "b.Target"}}}
	m := NewManager(&stubEmbedder{}, WithEdges(aug))
	ctx := context.Background()
	if m.Precise(dir) {
		t.Error("未建索引目录 Precise 应为 false")
	}
	if _, err := m.Search(ctx, dir, "target", 5, 0); err != nil {
		t.Fatal(err)
	}
	if !m.Precise(dir) {
		t.Error("精确边应用后 Precise 应为 true")
	}
	self, in, out, cands, ok, err := m.SymbolContext(ctx, dir, "b.Target")
	if err != nil || !ok {
		t.Fatalf("SymbolContext: ok=%v err=%v", ok, err)
	}
	if cands != 1 || len(in) != 1 || in[0].Symbol != "a.Caller" || in[0].Rel != relCaller || len(out) != 0 {
		t.Errorf("精确边邻接: self=%+v in=%+v out=%+v cands=%d", self, in, out, cands)
	}
	hits, err := m.Search(ctx, dir, "target", 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *Hit
	for i := range hits {
		if hits[i].Symbol == "b.Target" {
			target = &hits[i]
		}
	}
	if target == nil || len(target.Neighbors) != 1 ||
		target.Neighbors[0].Symbol != "a.Caller" || target.Neighbors[0].Rel != relCaller {
		t.Errorf("search 命中邻接应来自精确边: %+v", hits)
	}
	// 失败降级：另一目录 augmenter 失败 → 词法模式，Precise=false。
	failDir := t.TempDir()
	writeFileSet(t, failDir, map[string]string{
		"c/c.go": "package c\n\nfunc Solo() {}\n",
	})
	m2 := NewManager(&stubEmbedder{}, WithEdges(&stubAugmenter{err: errors.New("编译失败")}))
	if _, err := m2.Search(ctx, failDir, "solo", 5, 0); err != nil {
		t.Fatal(err)
	}
	if m2.Precise(failDir) {
		t.Error("augmenter 失败后 Precise 应为 false（降级词法）")
	}
}

// TestGraphOnlyMode 纯图模式（v5.11.0）：NewIndex(nil) 合法——Refresh 照常扫
// 文件抽 chunk/边/建图（Path/SymbolContext 正常、Size 照常、vecs 置 nil）、
// Search/Manager.Search 报「语义检索未启用」（Index.Search 单源拦截）、Precise
// 反映 augmenter 结果（nil augmenter 恒 false）。审查管线（runner→requirement）
// 用本形态接图：图查询零 embedding 依赖，不被 ARGUS_EMBEDDING_* 绑架。
func TestGraphOnlyMode(t *testing.T) {
	dir := graphFixture(t)
	ix := NewIndex(nil)
	ctx := context.Background()
	if err := ix.Refresh(ctx, dir); err != nil {
		t.Fatal(err)
	}
	// 图查询正常：Path 最短链与 SymbolContext 同 embedder 模式。
	chain, ok := ix.Path("Alpha", "Delta", graphHopsDefault)
	if !ok {
		t.Fatal("纯图模式 Alpha→Delta 应可达")
	}
	assertChain(t, chain,
		[]string{"Alpha", "Beta", "Gamma", "Delta"},
		[]string{"", relCalls, relCalls, relCalls})
	self, in, out, cands, ok := ix.SymbolContext("Beta")
	if !ok || cands != 1 {
		t.Fatalf("纯图模式 SymbolContext: ok=%v cands=%d", ok, cands)
	}
	if self.Symbol != "Beta" || self.Path != "a.go" || self.Line != 9 {
		t.Errorf("self = %+v, want Beta a.go:9", self)
	}
	if len(in) != 1 || in[0].Symbol != "Alpha" || in[0].Rel != relCaller ||
		len(out) != 2 || out[0].Symbol != "Gamma" || out[1].Symbol != "util.Gamma" {
		t.Errorf("in=%+v out=%+v", in, out)
	}
	// embedding 步确实跳过：落地 fileIndex 的 vecs 置 nil；Size/Truncated 照常。
	ix.mu.Lock()
	vecsNil := ix.files["a.go"] != nil && ix.files["a.go"].vecs == nil
	ix.mu.Unlock()
	if !vecsNil {
		t.Error("纯图模式 vecs 应置 nil（embedding 步跳过）")
	}
	if ix.Size() == 0 {
		t.Error("Size 照常（chunk 照常落地）")
	}
	if ix.Truncated() {
		t.Error("小夹具不应触达上限")
	}
	// Search 拦截（Index 直用）与 Manager 路径（经 Index.Search 同一拦截单源）。
	if _, err := ix.Search(ctx, "db", 3, 0); err == nil || !strings.Contains(err.Error(), "语义检索未启用") {
		t.Errorf("纯图模式 Search 应报「语义检索未启用」, got %v", err)
	}
	m := NewManager(nil)
	if _, err := m.Search(ctx, dir, "db", 3, 0); err == nil || !strings.Contains(err.Error(), "语义检索未启用") {
		t.Errorf("纯图 Manager.Search 应同款报错, got %v", err)
	}
	// Manager 图查询面照常（Trace/SymbolContext 不碰 embedder）。
	if _, found, err := m.Trace(ctx, dir, "Alpha", "Gamma"); err != nil || !found {
		t.Errorf("纯图 Manager.Trace: found=%v err=%v", found, err)
	}
	// Precise 读面：nil augmenter 恒 false；注入 augmenter 成功后 true。
	if ix.Precise() {
		t.Error("nil augmenter 时 Index.Precise 应为 false")
	}
	if m.Precise(dir) {
		t.Error("nil augmenter 时 Manager.Precise 应为 false")
	}
	aug := &stubAugmenter{edges: [][2]string{{"util.Gamma", "Delta"}}}
	m2 := NewManager(nil, WithEdges(aug))
	if _, _, err := m2.Trace(ctx, dir, "Alpha", "Gamma"); err != nil {
		t.Fatal(err)
	}
	if !m2.Precise(dir) {
		t.Error("精确边应用后 Manager.Precise 应为 true（纯图模式同款判定）")
	}
}
