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

// TestLayoutSVG_DenseRankSplitsSubColumns 超员拆列：47 节点的中间列拆成
// ≤14 的子列（多 x），14 节点改动列不拆；画布高度从 ~2000 降到千内量级。
func TestLayoutSVG_DenseRankSplitsSubColumns(t *testing.T) {
	// 中列 30 个表（>14 触发拆列），左右各少量节点。
	nodes := []Node{
		{ID: "file:a.go", Kind: KindFile, Changed: true},
		{ID: "file:b.go", Kind: KindFile, Changed: true},
	}
	for i := 0; i < 30; i++ {
		nodes = append(nodes, Node{ID: "table:t" + string(rune('a'+i)), Kind: KindTable})
	}
	nodes = append(nodes, Node{ID: "file:z.go", Kind: KindFile})
	var edges []Edge
	for _, n := range nodes {
		if n.Kind == KindTable {
			edges = append(edges, Edge{From: "file:a.go", To: n.ID, Rel: "lineage"})
			edges = append(edges, Edge{From: "file:b.go", To: n.ID, Rel: "lineage"})
		}
	}
	edges = append(edges, Edge{From: "table:ta", To: "file:z.go", Rel: "lineage"})
	pos, _, h := layoutSVG(nodes, edges)
	xs := map[int]int{}
	for _, p := range pos {
		xs[p[0]]++
	}
	// 表层拆成多个子列（30 > 14 → ≥3 个子列 x）。
	if len(xs) < 4 {
		t.Fatalf("30 节点中列应拆子列: x 分布 %+v", xs)
	}
	// 画布高度：拆后 ≤14 行量级 + 总线预留，远低于 30 行平铺的 ~1400。
	if h > 1100 {
		t.Fatalf("拆列后画布高度应收敛: h=%d", h)
	}
	// 改动列（2 节点）不拆：file:a/b 同 x。
	if pos["file:a.go"][0] != pos["file:b.go"][0] {
		t.Fatalf("未超员列不应拆: %+v", pos)
	}
}

// TestLayoutSVG_VariableHeightNoOverlap 变高行距：度大的高盒（54px）之后
// 的节点必须让位（y ≥ 上一盒底 + rowGap），不再压进固定 42px 节距。
func TestLayoutSVG_VariableHeightNoOverlap(t *testing.T) {
	// a 与 6 个表全连 → 度 6 → 高 54；b 只连 a → 高 30。
	nodes := []Node{
		{ID: "file:a.go", Kind: KindFile, Changed: true},
		{ID: "file:b.go", Kind: KindFile},
	}
	for i := 0; i < 6; i++ {
		nodes = append(nodes, Node{ID: "table:t" + string(rune('a'+i)), Kind: KindTable})
	}
	var edges []Edge
	for i := 0; i < 6; i++ {
		edges = append(edges, Edge{From: "file:a.go", To: "table:t" + string(rune('a'+i)), Rel: "lineage"})
	}
	edges = append(edges, Edge{From: "file:a.go", To: "file:b.go", Rel: "lineage"})
	pos, _, _ := layoutSVG(nodes, edges)
	// a 的度 6 → h=54；b 是 a 的邻居（同列相邻行）——行距按实际高推进。
	hA := nodeBoxHeight(6)
	if hA != 54 {
		t.Fatalf("度6 应 54px: %d", hA)
	}
	// a 与 b 同列相邻（按 weight 排序 file 在表前）：验证 b 在 a 之下且不重叠。
	yA, yB := pos["file:a.go"][1], pos["file:b.go"][1]
	if yB <= yA {
		// a/b 都是 file kind，排序按 id：a 在 b 前。
		t.Fatalf("行序: a=%d b=%d", yA, yB)
	}
	if yB < yA+hA+rowGap {
		t.Fatalf("高盒(%d)后邻行必须让位: yA=%d yB=%d 需 ≥%d", hA, yA, yB, yA+hA+rowGap)
	}
}
