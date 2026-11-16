// html_graph.go —— 交互视图剪枝与符号折叠：度数统计 / 列 0 超载判定 / 视图边重定向。

package impactview

import (
	"encoding/json"
	"fmt"
	"strings"
)

// viewGraphSymbolsCap 列 0 改动节点（symbol + file）上限：超出时 symbol 折叠进
// 父文件节点（同文件多 symbol 合一）。实测 27 symbol + 7 file = 34 节点列 0
// 在 1080p 视口不可读（边交叉全糊）；折叠后 7 file 节点 + 重定向边即可读。
const viewGraphSymbolsCap = 12

// viewGraph 交互视图的剪枝集（与 mermaid 同纪律，v3.185.2）：孤立改动符号
// （度=0——类型/常量被触达但无人调用）不进图，改动列从 16 盒收敛到 6 盒。
// 返回视图节点集与 (原始边序, 边) 对——data-i 须指向完整 s.Edges,JS 搜索
// 联动按它回查内嵌全量数据。
//
// v6.9.1 混合语言 PR 可读性：列 0 改动节点超 viewGraphSymbolsCap 时，symbol
// 折叠进父文件节点（Detail 记 "N 函数被触达"），边重定向 file→caller 并去重。
// 只影响视图剪枝集——快照 JSON 全量保留（JS 搜索/侧栏数据面不回退）。
func viewGraph(s *Snapshot) ([]Node, []int, []Edge, map[string][]string) {
	deg := nodeDegrees(s)
	collapseSyms, symToFile := collapsePlan(s, deg)
	nodes, symFoldNames := pruneViewNodes(s, deg, collapseSyms, symToFile)
	idxs, edges := pruneViewEdges(s, deg, collapseSyms, symToFile)
	return nodes, idxs, edges, symFoldNames
}

// nodeDegrees 节点度数表（边两端计数；孤立符号/超载判定的输入）。
func nodeDegrees(s *Snapshot) map[string]int {
	deg := map[string]int{}
	for _, e := range s.Edges {
		deg[e.From]++
		deg[e.To]++
	}
	return deg
}

// collapsePlan 列 0 超载判定：changed symbol + changed file 总数超
// viewGraphSymbolsCap 时折叠 symbol 进父文件；返回折叠开关与
// symID→父文件 ID 映射（sym:path:FuncName → file:path）。
func collapsePlan(s *Snapshot, deg map[string]int) (bool, map[string]string) {
	// ---- v6.9.1 列 0 超载判定：changed symbol + changed file 计数 ----
	changedSymCount := 0
	changedFileCount := 0
	for _, n := range s.Nodes {
		if !n.Changed {
			continue
		}
		switch n.Kind {
		case KindSymbol:
			if deg[n.ID] > 0 {
				changedSymCount++
			}
		case KindFile:
			changedFileCount++
		}
	}
	collapseSyms := changedSymCount+changedFileCount > viewGraphSymbolsCap && changedSymCount > 0

	// symID → parent file ID 映射（折叠用）：sym:path:FuncName → file:path
	symToFile := map[string]string{}
	if collapseSyms {
		for _, n := range s.Nodes {
			if n.Kind == KindSymbol && n.Changed && deg[n.ID] > 0 {
				if parts := strings.SplitN(n.ID, ":", 3); len(parts) == 3 {
					symToFile[n.ID] = "file:" + parts[1]
				}
			}
		}
	}
	return collapseSyms, symToFile
}

// pruneViewNodes 节点剪枝（symbol 折叠进父文件 + 无测试包标注）；返回视图
// 节点集与被折叠函数名表（侧栏展示用）。
func pruneViewNodes(s *Snapshot, deg map[string]int, collapseSyms bool, symToFile map[string]string) ([]Node, map[string][]string) {
	// ---- 节点剪枝（symbol 折叠进父文件）----
	nodes := make([]Node, 0, len(s.Nodes))
	noTest := map[string]bool{}
	for _, p := range s.Packages {
		if p.Tests == 0 {
			noTest["pkg:"+p.Path] = true
		}
	}
	// 折叠计数：fileID → 被折叠的 symbol 数 + 函数名列表
	symFoldCount := map[string]int{}
	symFoldNames := map[string][]string{} // v6.9.2 方向 A：侧栏展示被折叠函数
	for _, n := range s.Nodes {
		if n.Kind == KindSymbol && deg[n.ID] == 0 {
			continue
		}
		if collapseSyms && n.Kind == KindSymbol && n.Changed {
			if parentID, ok := symToFile[n.ID]; ok {
				symFoldCount[parentID]++
				// 提取函数名：sym:path:FuncName → FuncName
				if parts := strings.SplitN(n.ID, ":", 3); len(parts) == 3 {
					symFoldNames[parentID] = append(symFoldNames[parentID], parts[2])
				}
				continue // 折叠：不单独进图
			}
		}
		// v5.7.4 包节点标注测试覆盖缺口（风险面 TestMissing 的图上直读形态）
		if n.Kind == KindPackage && noTest[n.ID] && !strings.HasSuffix(n.Label, "·无测试") {
			n.Label += " ·无测试"
		}
		nodes = append(nodes, n)
	}
	// 折叠后的文件节点 Detail 补充 symbol 计数 + 被折叠函数名列表（侧栏展示用）
	if collapseSyms {
		for i, n := range nodes {
			if cnt, ok := symFoldCount[n.ID]; ok && cnt > 0 {
				nodes[i].Detail = fmt.Sprintf("%d 函数被触达", cnt)
			}
		}
	}
	return nodes, symFoldNames
}

// pruneViewEdges 边剪枝 + 折叠重定向：端点被剪的边剔除，折叠时 sym→caller
// 改挂 file→caller 并按 (from,to) 去重；返回原始边序下标与视图边集
// （data-i 回查快照全量边，JS 搜索联动按它取 data.edges）。
func pruneViewEdges(s *Snapshot, deg map[string]int, collapseSyms bool, symToFile map[string]string) ([]int, []Edge) {
	// ---- 边剪枝 + 折叠重定向 ----
	// 折叠时 sym→caller 边改为 file→caller（按 (from,to) 去重）
	type edgeKey struct{ from, to string }
	seenEdge := map[edgeKey]bool{}
	idxs := make([]int, 0, len(s.Edges))
	edges := make([]Edge, 0, len(s.Edges))
	for i, e := range s.Edges {
		if deg[e.From] == 0 || deg[e.To] == 0 {
			continue // 端点被剪（防御：孤立符号本应无边）
		}
		from, to := e.From, e.To
		if collapseSyms {
			if parentID, ok := symToFile[from]; ok {
				from = parentID // 折叠：sym→caller 重定向为 file→caller
			}
		}
		k := edgeKey{from, to}
		if seenEdge[k] {
			continue // 折叠产生的重复边去重
		}
		seenEdge[k] = true
		idxs = append(idxs, i)
		edges = append(edges, Edge{From: from, To: to, Rel: e.Rel})
	}
	return idxs, edges
}

// foldedSymsJSON 被折叠函数名列表的 JSON 序列化（侧栏展示用）。
// key = fileID（"file:path"），value = 被折叠的函数名列表。
func foldedSymsJSON(m map[string][]string) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return strings.ReplaceAll(string(b), "</", `<\/`)
}
