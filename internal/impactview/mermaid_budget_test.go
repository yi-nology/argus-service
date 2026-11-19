package impactview

import (
	"strings"
	"testing"
)

// TestTrimCallerEdgesBudget 评论图 caller 预算（v5.6.3）：总数封顶+每符号封顶，
// 非 caller 边一律不动，纯函数不污染输入切片。
func TestTrimCallerEdgesBudget(t *testing.T) {
	edges := []Edge{
		{From: "sym:a", To: "file:1", Rel: RelCaller},
		{From: "sym:a", To: "file:2", Rel: RelCaller},
		{From: "sym:a", To: "file:3", Rel: RelCaller},
		{From: "sym:a", To: "file:4", Rel: RelCaller}, // 第 4 条：每符号封顶剪
		{From: "file:x", To: "file:y", Rel: RelSource},
		{From: "file:x", To: "table:t", Rel: RelLineage},
	}
	for i := range 30 {
		edges = append(edges, Edge{From: "sym:b" + string(rune('a'+i%26)), To: "file:o", Rel: RelCaller})
	}
	inLen := len(edges)
	out, trimmed := trimCallerEdgesForComment(edges)
	if len(out)+trimmed != inLen {
		t.Fatalf("修剪应守恒: out=%d trimmed=%d in=%d", len(out), trimmed, inLen)
	}
	nonCaller := 0
	caller := map[string]int{}
	for _, e := range out {
		if e.Rel != RelCaller {
			nonCaller++
			continue
		}
		caller[e.From]++
		if caller[e.From] > maxCallerPerSym {
			t.Fatalf("单符号 caller 边超 %d: %s", maxCallerPerSym, e.From)
		}
	}
	if nonCaller != 2 {
		t.Fatalf("非 caller 边必须全保留: %d", nonCaller)
	}
	if total := len(out) - nonCaller; total > maxCallerEdgesComment {
		t.Fatalf("caller 总预算超 %d: %d", maxCallerEdgesComment, total)
	}
	if len(edges) != inLen {
		t.Fatal("输入切片被污染（纯函数纪律）")
	}
}

// TestPrioritizeNodesDepthAware 节点截断同层按依赖跳数浅者优先：直接受影响
// 的包先于深层传递者存活（此前同层只按 ID 字典序，深层包可能把浅层挤掉）；
// 跨层权重序（改动>包>测试>其他）不变。
func TestPrioritizeNodesDepthAware(t *testing.T) {
	nodes := []Node{
		{ID: "pkg:zzz/dep", Kind: KindPackage, Depth: 3},
		{ID: "pkg:aaa/dep", Kind: KindPackage, Depth: 3},
		{ID: "pkg:mmm/direct", Kind: KindPackage, Depth: 1},
		{ID: "file:a.go", Kind: KindFile, Changed: true},
		{ID: "test:b_test.go", Kind: KindTest},
	}
	got := prioritizeNodes(nodes, 2)
	if len(got) != 2 {
		t.Fatalf("应保留 2 个: %+v", got)
	}
	if got[0].ID != "file:a.go" || got[1].ID != "pkg:mmm/direct" {
		t.Fatalf("权重序+深度序不符: %v", []string{got[0].ID, got[1].ID})
	}
	// 同深度保 ID 字典序确定性
	got = prioritizeNodes(nodes, 4)
	if got[1].ID != "pkg:mmm/direct" || got[2].ID != "pkg:aaa/dep" || got[3].ID != "pkg:zzz/dep" {
		t.Fatalf("同层深度/ID 序不符: %v %v %v", got[1].ID, got[2].ID, got[3].ID)
	}
	// 纯函数纪律：输入不被就地重排污染
	if nodes[0].ID != "pkg:zzz/dep" {
		t.Fatal("输入切片被污染")
	}
}

// TestTailStrRuneSafe 尾段截取的 UTF-8 边界安全：截断点落在多字节字符中间
// 时回退到合法边界，不产出替换符。
func TestTailStrRuneSafe(t *testing.T) {
	s := strings.Repeat("a", 100) + "深" + strings.Repeat("b", 99) // 深=3 字节
	got := tailStr(s, 100)
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("截断点腰斩多字节字符: %q", got)
	}
	if len(got) > 100 {
		t.Fatalf("尾段超长: %d", len(got))
	}
	if tailStr("short", 100) != "short" {
		t.Fatal("短串应原样返回")
	}
}
