package impactview

// html_layout_test.go —— 分层布局判据：零改动静图（仓库地图共享视图）
// 必须分列——BFS 种子缺位曾让全部节点落尾列成单列堆叠
//（10.44.129.215 /repos/map?share=… 实弹缺陷）。

import "testing"

// TestLayoutSVG_ZeroChangedSpreadsColumns 0 改动 +目录包含边：以入度 0
// 根节点为种子分层，不得退化为单列。
func TestLayoutSVG_ZeroChangedSpreadsColumns(t *testing.T) {
	nodes := []Node{
		{ID: "pkg:.", Kind: KindPackage, Label: "."},
		{ID: "pkg:cmd", Kind: KindPackage, Label: "cmd"},
		{ID: "pkg:ddl", Kind: KindPackage, Label: "ddl"},
		{ID: "pkg:migrations", Kind: KindPackage, Label: "migrations"},
		{ID: "pkg:cmd/iam-apply", Kind: KindPackage, Label: "iam-apply"},
	}
	edges := []Edge{
		{From: "pkg:.", To: "pkg:cmd", Rel: RelContains},
		{From: "pkg:.", To: "pkg:ddl", Rel: RelContains},
		{From: "pkg:.", To: "pkg:migrations", Rel: RelContains},
		{From: "pkg:cmd", To: "pkg:cmd/iam-apply", Rel: RelContains},
	}
	pos, _, _ := layoutSVG(nodes, edges)
	xs := map[int]int{} // x → 该列节点数
	for _, p := range pos {
		xs[p[0]]++
	}
	if len(xs) < 2 {
		t.Fatalf("零改动静图应分列（入度0种子分层），实际单列: %+v", pos)
	}
	// 根在最左列，子层向右展开。
	if pos["pkg:."][0] >= pos["pkg:ddl"][0] {
		t.Fatalf("根应位于更左列: root=%v ddl=%v", pos["pkg:."], pos["pkg:ddl"])
	}
	if pos["pkg:cmd/iam-apply"][0] <= pos["pkg:cmd"][0] {
		t.Fatalf("子节点应在父节点右列: cmd=%v iam-apply=%v", pos["pkg:cmd"], pos["pkg:cmd/iam-apply"])
	}
}

// TestLayoutSVG_ChangedStillColumnZero 影响面语义回归：有改动节点时
// 改动必须钉在列 0（种子优先级不变）。
func TestLayoutSVG_ChangedStillColumnZero(t *testing.T) {
	nodes := []Node{
		{ID: "file:a.go", Kind: KindFile, Changed: true},
		{ID: "file:b.go", Kind: KindFile},
		{ID: "file:c.go", Kind: KindFile},
	}
	edges := []Edge{
		{From: "file:a.go", To: "file:b.go", Rel: "imports"},
		{From: "file:b.go", To: "file:c.go", Rel: "imports"},
	}
	pos, _, _ := layoutSVG(nodes, edges)
	if pos["file:a.go"][0] != pos["file:b.go"][0]-nodeW-colGap &&
		pos["file:a.go"][0] >= pos["file:b.go"][0] {
		t.Fatalf("改动节点应在最左列: %+v", pos)
	}
	if pos["file:a.go"][0] != 24 { // orgX：列 0 原点
		t.Fatalf("改动列应为列 0（x=orgX）: %+v", pos)
	}
}

// TestLayoutSVG_FullCycleSeed 无入度 0 节点（纯环）：字典序最小节点
// 保底做种子，仍不得单列退化到无结构。
func TestLayoutSVG_FullCycleSeed(t *testing.T) {
	nodes := []Node{
		{ID: "pkg:a", Kind: KindPackage},
		{ID: "pkg:b", Kind: KindPackage},
		{ID: "pkg:c", Kind: KindPackage},
	}
	edges := []Edge{
		{From: "pkg:a", To: "pkg:b", Rel: "imports"},
		{From: "pkg:b", To: "pkg:c", Rel: "imports"},
		{From: "pkg:c", To: "pkg:a", Rel: "imports"},
	}
	pos, _, _ := layoutSVG(nodes, edges)
	xs := map[int]bool{}
	for _, p := range pos {
		xs[p[0]] = true
	}
	if len(xs) < 2 {
		t.Fatalf("纯环图应有保底种子分层: %+v", pos)
	}
}
