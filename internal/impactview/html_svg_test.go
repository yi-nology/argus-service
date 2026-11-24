package impactview

// html_svg_test.go —— 跨列边总线路由：越距边走图下正交总线（垫底绘制
// 曾让越列边在邻列盒子后消失），邻距边保持贝塞尔。

import (
	"strings"
	"testing"
)

func TestSVGEdges_CrossColumnUsesBus(t *testing.T) {
	// 两列布局：a(col0) → t(col2 越过 col1)；n(col1) 相邻正常。
	pos := map[string][2]int{
		"file:a.go":    {24, 24},
		"table:mid":    {334, 24},
		"table:far":    {954, 24}, // 距 col0 两槽（越列）
		"file:near.go": {644, 24},
	}
	edges := []Edge{
		{From: "file:a.go", To: "table:far", Rel: "lineage"},    // 跨列 → 总线
		{From: "table:mid", To: "file:near.go", Rel: "lineage"}, // 相邻 → 贝塞尔
	}
	var b strings.Builder
	writeSVGEdges(&b, pos, edges, []int{0, 1}, map[string]int{}, map[string]bool{})
	out := b.String()
	lines := strings.Split(out, "\n")
	if len(lines) < 2 {
		t.Fatalf("应产出两条边: %q", out)
	}
	bus, bezier := lines[0], lines[1]
	if !strings.Contains(bus, " L") || strings.Contains(bus, " C") {
		t.Fatalf("越列边应走正交总线（L 段、无 C）: %s", bus)
	}
	if !strings.Contains(bezier, " C") {
		t.Fatalf("相邻边应保持贝塞尔: %s", bezier)
	}
	// 总线车道在图盒底之下（pos 最低 y=24 + 高 30 = 54；车道 ≥ 54+24）。
	if !strings.Contains(bus, ",78 L") && !strings.Contains(bus, ",82 L") {
		// 首车道 = bottom(54) + 24 = 78。
		t.Fatalf("车道应在图盒底之下: %s", bus)
	}
}

func TestSVGEdges_ReverseLongEdgeUsesBus(t *testing.T) {
	// 反向越列边（深列 → 浅列）：出源左边、入目标右边，同样走总线。
	pos := map[string][2]int{
		"table:deep": {644, 24},
		"file:top":   {24, 24},
	}
	edges := []Edge{{From: "table:deep", To: "file:top", Rel: "lineage"}}
	var b strings.Builder
	writeSVGEdges(&b, pos, edges, []int{0}, map[string]int{}, map[string]bool{})
	out := b.String()
	if !strings.Contains(out, " L") || strings.Contains(out, " C") {
		t.Fatalf("反向越列边应走总线: %s", out)
	}
	// 出源左边（644）入目标右边（24+240=264）。
	if !strings.Contains(out, "M644,") {
		t.Fatalf("应从源左边出: %s", out)
	}
}
