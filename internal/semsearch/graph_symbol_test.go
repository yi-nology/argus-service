package semsearch

import (
	"context"
	"testing"
)

// TestGraph_SiblingAndFallback 同接收者兄弟方法（跨文件同目录键）+ qualified
// 目标缺失 bare 回退 + dot-import 跨包裸调用回退（v5.10.0）。
func TestGraph_SiblingAndFallback(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"go.mod":           "module sib\n\ngo 1.22\n",
		"svc/run.go":       "package svc\n\nfunc (s *Runner) Run() {\n\ts.start()\n}\n",
		"svc/start.go":     "package svc\n\nfunc (r *Runner) start() {}\n",
		"helper/helper.go": "package helper\n\nfunc Util() {}\n",
		"dot.go": `package main

import . "sib/helper"

func UseDot() {
	Util()
}
`,
		"qual.go": `package main

import "sib/gone"

func Caller() {
	gone.Missing()
}

func Missing() {}
`,
	})
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 兄弟方法：start 定义在同包其他文件（start.go），同目录键精确命中。
	self, in, _, cands, ok := ix.SymbolContext("svc.Runner.start")
	if !ok || cands != 1 {
		t.Fatalf("svc.Runner.start 应单候选命中: ok=%v cands=%d", ok, cands)
	}
	if self.Symbol != "svc.Runner.start" || self.Path != "svc/start.go" || self.Line != 3 {
		t.Errorf("self = %+v, want svc.Runner.start svc/start.go:3", self)
	}
	if got := refSymbols(in); len(got) != 1 || got[0] != "svc.Runner.Run" {
		t.Errorf("in = %v, want [svc.Runner.Run]（同接收者兄弟方法）", got)
	}
	// qualified 目标缺失（import 了不存在的 sib/gone）→ bare 末段回退命中根包
	// 同名 Missing。
	ix.mu.Lock()
	callerOut := append([]string(nil), ix.sym.adj["Caller"]...)
	ix.mu.Unlock()
	if len(callerOut) != 1 || callerOut[0] != "Missing" {
		t.Errorf("Caller 被调 = %v, want [Missing]（qualified 缺失降 bare 回退）", callerOut)
	}
	// dot-import：跨包裸调用同目录无候选 → 全局 bare 回退保召回。
	ix.mu.Lock()
	dotOut := append([]string(nil), ix.sym.adj["UseDot"]...)
	ix.mu.Unlock()
	if len(dotOut) != 1 || dotOut[0] != "helper.Util" {
		t.Errorf("UseDot 被调 = %v, want [helper.Util]（dot-import 走全局回退）", dotOut)
	}
}

// TestGraph_SymbolContext 单符号上下文：同名多候选（异接收者方法）self 取路径
// 序首个、邻接合并、候选数；精确全名单候选；未知/stdlib 裸名 ok=false。
func TestGraph_SymbolContext(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"x.go": `package x

func (s *Store) Save() {
	s.touch()
}

func (s *Store) touch() {}
`,
		"y.go": `package x

func (c *Client) Save() {
	c.flush()
}

func (c *Client) flush() {}
`,
		"callers.go": `package x

func CallerA() {
	st := &Store{}
	st.Save()
}

func CallerB() {
	cl := &Client{}
	cl.Save()
}
`,
	})
	ix := NewIndex(&stubEmbedder{})
	if err := ix.Refresh(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	// 裸名 Save 两候选（根目录键 Client.Save / Store.Save），词法级合并邻接
	// ——st/cl 是 var 限定（类型不可见），降裸名末段保召回。
	self, in, out, cands, ok := ix.SymbolContext("Save")
	if !ok {
		t.Fatal("Save 应命中")
	}
	if cands != 2 {
		t.Errorf("Save cands = %d, want 2", cands)
	}
	if self.Symbol != "Client.Save" || self.Path != "y.go" || self.Line != 3 {
		t.Errorf("self = %+v, want Client.Save y.go:3（路径序首个候选）", self)
	}
	if got := refSymbols(in); len(got) != 2 || got[0] != "CallerA" || got[1] != "CallerB" {
		t.Errorf("Save in = %v, want [CallerA CallerB]（多候选合并）", got)
	}
	for _, r := range in {
		if r.Rel != relCaller {
			t.Errorf("Save in rel = %q, want %q", r.Rel, relCaller)
		}
	}
	if got := refSymbols(out); len(got) != 2 || got[0] != "Client.flush" || got[1] != "Store.touch" {
		t.Errorf("Save out = %v, want [Client.flush Store.touch]", got)
	}
	for _, r := range out {
		if r.Rel != relCallee {
			t.Errorf("Save out rel = %q, want %q", r.Rel, relCallee)
		}
	}
	// 精确全名：单候选（s.touch/c.flush 经兄弟方法规则 qualified 精确落边）。
	self2, in2, out2, cands2, ok2 := ix.SymbolContext("Store.touch")
	if !ok2 || cands2 != 1 || self2.Symbol != "Store.touch" || self2.Path != "x.go" || self2.Line != 7 {
		t.Errorf("Store.touch self = %+v cands=%d ok=%v", self2, cands2, ok2)
	}
	if len(in2) != 1 || in2[0].Symbol != "Store.Save" || in2[0].Rel != relCaller {
		t.Errorf("Store.touch in = %+v", in2)
	}
	if len(out2) != 0 {
		t.Errorf("Store.touch out = %+v, want 空", out2)
	}
	// stdlib 裸名与未知符号都不在符号表（cands=0）。
	if _, _, _, c, ok := ix.SymbolContext("Println"); ok || c != 0 {
		t.Error("stdlib 裸名 Println 不应在符号表")
	}
	if _, _, _, c, ok := ix.SymbolContext("NoSuch"); ok || c != 0 {
		t.Error("未知符号应 ok=false")
	}
}

// TestSearchNeighbors 检索命中自动附邻接（图面接线）：Alpha 只带被调，Beta
// 调用方+被调合并按 Symbol 排序。
func TestSearchNeighbors(t *testing.T) {
	dir := t.TempDir()
	writeFileSet(t, dir, map[string]string{
		"app.go": `package app

// Alpha 查询 DB。
func Alpha() {
	Beta()
}

func Beta() {
	ParseAll()
}

func ParseAll() {}
`,
	})
	m := NewManager(&stubEmbedder{})
	hits, err := m.Search(context.Background(), dir, "db", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("hit 数 = %d, want 3: %+v", len(hits), hits)
	}
	var alpha, beta *Hit
	for i := range hits {
		switch hits[i].Symbol {
		case "Alpha":
			alpha = &hits[i]
		case "Beta":
			beta = &hits[i]
		}
	}
	if alpha == nil || beta == nil {
		t.Fatalf("应命中 Alpha 与 Beta: %+v", hits)
	}
	if len(alpha.Neighbors) != 1 || alpha.Neighbors[0].Symbol != "Beta" || alpha.Neighbors[0].Rel != relCallee {
		t.Errorf("Alpha 邻接 = %+v, want [{Beta callee}]", alpha.Neighbors)
	}
	if len(beta.Neighbors) != 2 ||
		beta.Neighbors[0].Symbol != "Alpha" || beta.Neighbors[0].Rel != relCaller ||
		beta.Neighbors[1].Symbol != "ParseAll" || beta.Neighbors[1].Rel != relCallee {
		t.Errorf("Beta 邻接 = %+v, want [{Alpha caller} {ParseAll callee}]", beta.Neighbors)
	}
}

// TestManagerTraceAndSymbolContext Manager 数据面：Trace 最短链、二次 Trace 零
// embedding（增量零成本）、不可达 false、SymbolContext 同款目录缓存模式。
func TestManagerTraceAndSymbolContext(t *testing.T) {
	dir := graphFixture(t)
	e := &stubEmbedder{}
	m := NewManager(e)
	ctx := context.Background()
	chain, ok, err := m.Trace(ctx, dir, "Alpha", "Gamma")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("Alpha→Gamma 应可达")
	}
	assertChain(t, chain, []string{"Alpha", "Beta", "Gamma"}, []string{"", relCalls, relCalls})
	callsAfterFirst := e.calls
	if _, _, err := m.Trace(ctx, dir, "Alpha", "Gamma"); err != nil {
		t.Fatal(err)
	}
	if e.calls != callsAfterFirst {
		t.Errorf("二次 Trace 不应触发 Embed: %d → %d", callsAfterFirst, e.calls)
	}
	if _, ok, _ := m.Trace(ctx, dir, "Alpha", "Lonely"); ok {
		t.Error("孤立符号不可达")
	}
	self, in, out, cands, ok, err := m.SymbolContext(ctx, dir, "Beta")
	if err != nil || !ok {
		t.Fatalf("SymbolContext 失败: ok=%v err=%v", ok, err)
	}
	if cands != 1 {
		t.Errorf("Beta cands = %d, want 1（根目录单候选）", cands)
	}
	if self.Symbol != "Beta" || self.Path != "a.go" || self.Line != 9 {
		t.Errorf("self = %+v, want Beta a.go:9", self)
	}
	if len(in) != 1 || in[0].Symbol != "Alpha" || in[0].Rel != relCaller {
		t.Errorf("in = %+v", in)
	}
	// Beta 同包裸调 Gamma + import 限定 util.Gamma：两个被调。
	if len(out) != 2 || out[0].Symbol != "Gamma" || out[0].Rel != relCallee ||
		out[1].Symbol != "util.Gamma" || out[1].Rel != relCallee {
		t.Errorf("out = %+v, want [Gamma util.Gamma]", out)
	}
}
