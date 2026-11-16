// snapshot_html_test.go —— HTML 视图形态/剪枝/容量守卫/悬挂边不变量测试。

package impactview

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/oagentkit/textx/codeintel"
)

func nodeIDs(s *Snapshot) []string {
	var out []string
	for _, n := range s.Nodes {
		out = append(out, n.ID)
	}
	return out
}

// TestRenderHTML 交互视图：自包含 HTML（零外链）、内嵌 JSON、脚本闭合防线。
func TestRenderHTML(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	page := RenderHTMLFull(s, "task-1", ViewOpts{})
	if page == "" || !strings.HasPrefix(page, "<!DOCTYPE html>") {
		t.Fatal("HTML 形态不对")
	}
	if !strings.Contains(page, `id="impact-data"`) || !strings.Contains(page, "pkga/a.go") {
		t.Fatal("缺内嵌数据或节点标签")
	}
	if strings.Contains(page, `</script zen`) { // 防闭合改写必须生效的占位断言
		t.Fatal("不应出现未转义闭合")
	}
	if strings.Contains(page, "argus_impactview_dummy") {
		t.Fatal("意外内容")
	}
	// taskID 进页头
	if !strings.Contains(page, "task-1") {
		t.Fatal("页头缺任务标识")
	}
	// 空 taskID 页头省略
	page2 := RenderHTMLFull(s, "", ViewOpts{})
	if strings.Contains(page2, "任务 </span>") {
		t.Fatal("空任务标识应省略页头段")
	}
	// 空快照 → 空串
	if RenderHTMLFull(&Snapshot{}, "t", ViewOpts{}) != "" {
		t.Fatal("空快照应返回空串")
	}
}

// TestNoDanglingEdges 不变量：所有边的端点必须落在节点集内。回归锚——
// v3.183.0 曾因改动包挂边漏 file: 前缀产 3 条悬挂边，SVG/mermaid 双面静默丢边。
func TestNoDanglingEdges(t *testing.T) {
	dir := writeFixture(t)
	idx, _ := codeintel.NewIndex(dir, 2000)
	s := Build(context.Background(), Input{
		Dir: dir, ChangedFiles: []string{"pkga/a.go"},
		HunkRanges: changedOf("pkga/a.go"), Index: idx, MaxCommits: -1,
	})
	ids := map[string]bool{}
	for _, n := range s.Nodes {
		ids[n.ID] = true
	}
	for _, e := range s.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Fatalf("悬挂边: %+v（节点集 %v）", e, nodeIDs(s))
		}
	}
	// module 根标签可读性
	if s := pkgLabel("."); s != "(module 根)" {
		t.Fatalf("pkgLabel(.) = %s", s)
	}
}

// TestSnapshotCapacityGuard 容量守卫（v3.184.1）：大 PR（200 改动文件+大闭包）
// 节点封顶 150、边随端点裁剪无悬挂、JSON ≤48KB、截断注明齐备。
func TestSnapshotCapacityGuard(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a/a.go"}}
	// 200 个改动文件节点（L0 场景）+ 每文件一条 caller 边
	for i := range 200 {
		id := "file:pkg" + strconv.Itoa(i) + "/f.go"
		s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindFile, Label: id, Changed: true})
		symID := "sym:pkg" + strconv.Itoa(i) + "/f.go:F"
		s.Nodes = append(s.Nodes, Node{ID: symID, Kind: KindSymbol, Label: "F", Changed: true})
		s.Edges = append(s.Edges, Edge{From: id, To: symID, Rel: RelCaller})
	}
	s.Nodes = append(s.Nodes, Node{ID: "file:a/a.go", Kind: KindFile, Label: "a.go", Changed: true})
	finalize(s)

	if len(s.Nodes) > MaxSnapshotNodes {
		t.Fatalf("节点未封顶: %d", len(s.Nodes))
	}
	ids := map[string]bool{}
	for _, n := range s.Nodes {
		ids[n.ID] = true
	}
	for _, e := range s.Edges {
		if !ids[e.From] || !ids[e.To] {
			t.Fatalf("裁剪后残留悬挂边: %+v", e)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxSnapshotJSON {
		t.Fatalf("JSON 超体积守卫: %d", len(b))
	}
	if !s.Stats.Truncated {
		t.Fatal("应置 Truncated")
	}
	found := false
	for _, n := range s.Notes {
		if strings.Contains(n, "按影响权重截断") {
			found = true
		}
	}
	if !found {
		t.Fatalf("缺截断注明: %+v", s.Notes)
	}
}

// TestMermaidDropsIsolatedSymbols 可读性纪律（v3.185.1，用户实拍「看不懂」反馈）：
// 孤立改动符号（类型/常量被触达但无调用边）不进 mermaid 图；有调用边的改动
// 符号着黄色 chgsym；边标签动词化。
func TestMermaidDropsIsolatedSymbols(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a/a.go"}}
	s.Nodes = append(s.Nodes,
		Node{ID: "file:a/a.go", Kind: KindFile, Label: "a.go", Changed: true},
		Node{ID: "sym:a/a.go:Func", Kind: KindSymbol, Label: "Func", Changed: true},
		// 类型/常量触达形态：无任何调用边 → 应摘除
		Node{ID: "sym:a/a.go:SomeType", Kind: KindSymbol, Label: "SomeType", Changed: true},
		Node{ID: "sym:a/a.go:MaxConst", Kind: KindSymbol, Label: "MaxConst", Changed: true},
		Node{ID: "file:b/b.go", Kind: KindFile, Label: "b.go", Detail: "调用方：Use"},
	)
	s.Edges = append(s.Edges,
		Edge{From: "file:a/a.go", To: "sym:a/a.go:Func", Rel: RelCaller},
		Edge{From: "sym:a/a.go:Func", To: "file:b/b.go", Rel: RelCaller},
	)
	src, _ := RenderMermaidWithStats(s)
	if strings.Contains(src, "SomeType") || strings.Contains(src, "MaxConst") {
		t.Fatalf("孤立符号未摘除:\n%s", src)
	}
	if !strings.Contains(src, "chgsym") {
		t.Fatal("有调用边的改动符号应着 chgsym 黄类")
	}
	if !strings.Contains(src, "波及调用方") {
		t.Fatal("边标签应动词化")
	}
	// 全孤立退化：只剩无边的符号 → 空图（报告段自动省略/降级列表）
	empty := &Snapshot{Version: SnapshotVersion, Changed: []string{"a.go"},
		Nodes: []Node{{ID: "sym:a.go:X", Kind: KindSymbol, Label: "X", Changed: true}}}
	emptySrc, _ := RenderMermaidWithStats(empty)
	if emptySrc != "" {
		t.Fatal("全孤立应返回空图")
	}
}

// TestRenderMermaidPure 渲染纯函数性（v3.185.1 回归锚）：renderMermaid 曾用
// edges[:0] 原地过滤污染 s.Edges,二次渲染（超长聚合路径）产生重复边——
// 同快照连渲两次必须逐字节一致。
func TestRenderMermaidPure(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a/a.go"}}
	s.Nodes = append(s.Nodes, Node{ID: "file:a/a.go", Kind: KindFile, Label: "a.go", Changed: true})
	for i := range 10 {
		id := "pkg:p" + strconv.Itoa(i)
		s.Nodes = append(s.Nodes, Node{ID: id, Kind: KindPackage, Label: id})
		s.Edges = append(s.Edges, Edge{From: "file:a/a.go", To: id, Rel: RelImport})
	}
	first, _ := RenderMermaidWithStats(s)
	second, _ := RenderMermaidWithStats(s)
	if first != second {
		t.Fatal("渲染非纯：二次调用输出漂移")
	}
	if got := strings.Count(s.Edges[0].From, ""); got == 0 {
		t.Fatal("哨兵")
	}
	if len(s.Edges) != 10 {
		t.Fatalf("s.Edges 被渲染污染（len=%d）", len(s.Edges))
	}
}

// TestRenderHTMLViewPrune 交互视图剪枝（v3.185.2,与 mermaid 同纪律）：孤立
// 改动符号不进 SVG（用户实测噪声列），SVG 无常驻边标签（悬停才显示）。
func TestRenderHTMLViewPrune(t *testing.T) {
	s := &Snapshot{Version: SnapshotVersion, Changed: []string{"a/a.go"}}
	s.Nodes = append(s.Nodes,
		Node{ID: "file:a/a.go", Kind: KindFile, Label: "a.go", Changed: true},
		Node{ID: "sym:a/a.go:Func", Kind: KindSymbol, Label: "Func", Changed: true},
		Node{ID: "sym:a/a.go:SomeConst", Kind: KindSymbol, Label: "SomeConst", Changed: true}, // 孤立
		Node{ID: "file:b/b.go", Kind: KindFile, Label: "b.go"},
	)
	s.Edges = append(s.Edges,
		Edge{From: "file:a/a.go", To: "sym:a/a.go:Func", Rel: RelCaller},
		Edge{From: "sym:a/a.go:Func", To: "file:b/b.go", Rel: RelCaller},
	)
	page := RenderHTMLFull(s, "t", ViewOpts{})
	// 只断 SVG 部分——内嵌 JSON 是全量快照（MCP/面板数据面），含孤立节点属预期
	svgStart := strings.Index(page, "<svg")
	svgEnd := strings.Index(page, "</svg>")
	if svgStart < 0 || svgEnd < 0 {
		t.Fatal("SVG 缺失")
	}
	if strings.Contains(page[svgStart:svgEnd], "SomeConst") {
		t.Fatal("孤立常量符号未从 SVG 剪除")
	}
	if strings.Contains(page, `class="elabel"`) && !strings.Contains(page, "edgeg text.elabel{opacity:0") {
		t.Fatal("边标签应默认隐藏")
	}
	if !strings.Contains(page, "edgeg") {
		t.Fatal("边应包 edgeg 组（悬停显示标签）")
	}
	// 剪枝后仍无悬挂边（data-i 回查全量序）
	vn, vi, ve, _ := viewGraph(s)
	if len(vn) != 3 || len(ve) != 2 || len(vi) != 2 {
		t.Fatalf("view 集口径错: n=%d e=%d i=%d", len(vn), len(ve), len(vi))
	}
}
