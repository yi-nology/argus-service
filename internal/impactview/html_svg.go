// html_svg.go —— SVG 绘制：边/节点/关键路径高亮/边频次粗细编码与标签截断。

package impactview

import (
	"fmt"
	"html"
	"strings"
)

// svgBody 产出 SVG 主体（框 + 标签 + 贝塞尔边）。
// viewNodes/viewEdges 是布局与绘制共用的剪枝后视图集（见 viewGraph）。
//
// v6.9.7 方向 U：边粗细编码调用频率——同一 (from,to) 对的边数越多线越粗。
// 改法：统计 viewEdges 中每个 (from,to) 的出现次数，画边时按次数设定
// stroke-width（1.2 + 0.4×(n-1)，封顶 3.0）。折叠重定向后的目录聚合
// 边自然变粗（多条 file→caller 合并为一条 file→dir）。
func svgBody(pos map[string][2]int, viewNodes []Node, idxs []int, viewEdges []Edge, fcount map[string]int, addedSet, removedSet map[string]bool) string {
	var b strings.Builder
	// 边先画（垫底）；data-i 携带快照边序——JS 搜索联动按它回查 data.edges,
	// 不依赖「DOM 序=数据序」的隐式对齐。边标签包 .edgeg 组默认隐藏、悬停
	// 显示（v3.185.2：20 条边的中点小字互相糊叠是实测遮挡源，关系详情走
	// tooltip 与点选面板）
	edgeFreq := edgeFrequencies(viewEdges)
	criticalEdges := criticalPathEdges(viewNodes, viewEdges)
	writeSVGEdges(&b, pos, viewEdges, idxs, edgeFreq, criticalEdges)
	writeSVGNodes(&b, pos, viewNodes, viewEdges, fcount, addedSet, removedSet)
	return b.String()
}

// edgeFrequencies 统计 (from,to) 边频次（方向 U 粗细编码的输入）。
func edgeFrequencies(viewEdges []Edge) map[string]int {
	edgeFreq := map[string]int{}
	for _, e := range viewEdges {
		edgeFreq[e.From+"\x00"+e.To]++
	}
	return edgeFreq
}

// criticalPathEdges 关键路径高亮：BFS 找改动节点到测试节点的最短路径，路径
// 上的边标记为关键边（帮 review 快速定位「改动 → 需跑测试」链路）。
func criticalPathEdges(viewNodes []Node, viewEdges []Edge) map[string]bool {
	criticalEdges := map[string]bool{}
	// 收集改动节点和测试节点
	var changedIDs, testIDs []string
	for _, n := range viewNodes {
		if n.Changed {
			changedIDs = append(changedIDs, n.ID)
		}
		if n.Kind == KindTest {
			testIDs = append(testIDs, n.ID)
		}
	}
	// BFS 从每个 changed 节点出发找最近 test 节点
	adj := map[string][]string{}
	for _, e := range viewEdges {
		adj[e.From] = append(adj[e.From], e.To)
		adj[e.To] = append(adj[e.To], e.From)
	}
	for _, start := range changedIDs {
		prev := map[string]string{start: ""}
		queue := []string{start}
		found := ""
		for len(queue) > 0 && found == "" {
			cur := queue[0]
			queue = queue[1:]
			for _, nx := range adj[cur] {
				if _, seen := prev[nx]; seen {
					continue
				}
				prev[nx] = cur
				for _, tid := range testIDs {
					if nx == tid {
						found = tid
						break
					}
				}
				if found != "" {
					break
				}
				queue = append(queue, nx)
			}
		}
		// 回溯路径标记边
		if found != "" {
			for cur := found; prev[cur] != ""; cur = prev[cur] {
				criticalEdges[prev[cur]+"\x00"+cur] = true
				criticalEdges[cur+"\x00"+prev[cur]] = true
			}
		}
	}
	return criticalEdges
}

// writeSVGEdges 绘制贝塞尔边：data-i 携带快照边序、边粗细按频次编码、
// 关键路径边加金色虚线高亮。
func writeSVGEdges(b *strings.Builder, pos map[string][2]int, viewEdges []Edge, idxs []int, edgeFreq map[string]int, criticalEdges map[string]bool) {
	for k, e := range viewEdges {
		i := idxs[k]
		p1, ok1 := pos[e.From]
		p2, ok2 := pos[e.To]
		if !ok1 || !ok2 {
			continue
		}
		x1 := p1[0] + nodeW
		y1 := p1[1] + nodeH/2
		x2 := p2[0]
		y2 := p2[1] + nodeH/2
		mid := (x1 + x2) / 2
		// 方向 U：边粗细 = 1.2 + 0.4×(freq-1)，封顶 3.0
		freq := edgeFreq[e.From+"\x00"+e.To]
		strokeW := 1.2 + 0.4*float64(freq-1)
		if strokeW > 3.0 {
			strokeW = 3.0
		}
		// v6.10.3 方向 AH：关键路径边加金色虚线高亮
		if criticalEdges[e.From+"\x00"+e.To] {
			fmt.Fprintf(b, `<g class="edgeg"><path data-i="%d" d="M%d,%d C%d,%d %d,%d %d,%d" class="edge rel-%s" style="stroke-width:%.1f;stroke:#f59e0b;stroke-dasharray:6,3"/><text class="elabel" x="%d" y="%d">%s</text></g>`+"\n",
				i, x1, y1, mid, y1, mid, y2, x2, y2, e.Rel, strokeW, mid, y2-6, html.EscapeString(mermaidRel(e.Rel)))
		} else {
			fmt.Fprintf(b, `<g class="edgeg"><path data-i="%d" d="M%d,%d C%d,%d %d,%d %d,%d" class="edge rel-%s" style="stroke-width:%.1f"/><text class="elabel" x="%d" y="%d">%s</text></g>`+"\n",
				i, x1, y1, mid, y1, mid, y2, x2, y2, e.Rel, strokeW, mid, y2-6, html.EscapeString(mermaidRel(e.Rel)))
		}
	}
}

// writeSVGNodes 绘制节点框：度数编码框高与风险热力 fill、tooltip 风险信号、
// 演化对比描边、findings 红点计数徽标。
func writeSVGNodes(b *strings.Builder, pos map[string][2]int, viewNodes []Node, viewEdges []Edge, fcount map[string]int, addedSet, removedSet map[string]bool) {
	for _, n := range viewNodes {
		p, ok := pos[n.ID]
		if !ok {
			continue
		}
		fill, stroke := nodeFill(n)
		label := truncateRunes(n.Label, 30)
		tip := n.Kind + " · " + n.ID
		if n.Detail != "" {
			tip += "\n" + n.Detail
		}
		// v6.9.7 方向 T：节点大小编码风险——文件节点边数（度）越大框越高。
		// 改法：统计 viewEdges 中每个节点的度，节点高度 = nodeH + min(degree,6)*4。
		// 度大 = 被更多人调用/影响更多人 = 风险 Concentration 的一手信号。
		deg := 0
		for _, e := range viewEdges {
			if e.From == n.ID || e.To == n.ID {
				deg++
			}
		}
		h := nodeH + min(deg, 6)*4
		// v6.10.1 方向 AI：风险热力图——非改动节点按 degree 归一化 fill 色蓝→红渐变。
		// 改法：计算全图 maxDeg，deg/maxDeg 插值 RGB（蓝 #3b82f6 → 红 #ef4444）。
		// changed 节点保持红色不变，不受影响。
		if !n.Changed && deg > 0 {
			maxDeg := 0
			for _, n2 := range viewNodes {
				d2 := 0
				for _, e2 := range viewEdges {
					if e2.From == n2.ID || e2.To == n2.ID {
						d2++
					}
				}
				if d2 > maxDeg {
					maxDeg = d2
				}
			}
			if maxDeg > 0 {
				ratio := float64(deg) / float64(maxDeg)
				r := int(59 + ratio*(239-59))
				g := int(130 - ratio*(130-68))
				b := int(246 - ratio*(246-68))
				fill = fmt.Sprintf("#%02x%02x%02x", r, g, b)
				stroke = fill
			}
		}
		// v6.9.8 方向 Y：tooltip 加风险信号——度数 + findings 数（已有徽标计数）
		tip += fmt.Sprintf("\n关联 %d", deg)
		if c := fcount[n.ID]; c > 0 {
			tip += fmt.Sprintf(" · 意见 %d", c)
		}
		// v6.10.6 方向 AQ：演化对比高亮——新增节点绿框 / 移除节点灰虚框
		strokeExtra := ""
		if addedSet[n.ID] {
			strokeExtra = ` stroke="#16a34a" stroke-width="3"`
			tip += "\n🆕 本轮新增波及"
		} else if removedSet[n.ID] {
			strokeExtra = ` stroke="#94a3b8" stroke-width="2" stroke-dasharray:4,3"`
			tip += "\n🔄 上轮已移除"
		}
		if strokeExtra != "" {
			fmt.Fprintf(b, `<g class="node kind-%s" data-id="%s" transform="translate(%d,%d)"><title>%s</title><rect width="%d" height="%d" rx="6" fill="%s" style="%s"/><text x="12" y="20">%s</text>`,
				n.Kind, html.EscapeString(n.ID), p[0], p[1],
				html.EscapeString(tip), nodeW, h, fill, strokeExtra, html.EscapeString(label))
		} else {
			fmt.Fprintf(b, `<g class="node kind-%s" data-id="%s" transform="translate(%d,%d)"><title>%s</title><rect width="%d" height="%d" rx="6" fill="%s" stroke="%s"/><text x="12" y="20">%s</text>`,
				n.Kind, html.EscapeString(n.ID), p[0], p[1],
				html.EscapeString(tip), nodeW, h, fill, stroke, html.EscapeString(label))
		}
		// v5.8.0 findings 徽标:有审查意见的文件节点右上角红点+计数
		if c := fcount[n.ID]; c > 0 {
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="9" fill="#dc2626"/><text x="%d" y="%d" text-anchor="middle" font-size="11" fill="#fff" pointer-events="none">%d</text>`,
				nodeW-6, 6, nodeW-6, 10, c)
		}
		fmt.Fprintf(b, `</g>`+"\n")
	}
}

// truncateRunes 显示截断（尾部省略号）。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
