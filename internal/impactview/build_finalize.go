// build_finalize.go —— 节点补充与快照收尾：symbol/coupling 节点 / 容量守卫 / 确定化。

package impactview

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// appendSymbolNodes L2 节点/边：符号节点 + 调用方文件节点（复用已有 file
// 节点）。L1 降级时调用方文件节点照建——文件层独立成立。返回调用位总数。
func appendSymbolNodes(s *Snapshot, syms []symHits) int {
	total := 0
	for _, h := range syms {
		display := h.Name
		if h.Recv != "" {
			display = h.Recv + "." + h.Name
		}
		symID := "sym:" + h.File + ":" + display
		s.Nodes = append(s.Nodes, Node{
			ID: symID, Kind: KindSymbol, Label: display, Changed: true,
			Detail: h.File + ":" + strconv.Itoa(h.Line),
		})
		for _, cs := range h.Callers {
			total++
			callerID := "file:" + cs.File
			if !s.hasNode(callerID) {
				detail := "调用方"
				if cs.Caller != "" && cs.Caller != "(package init)" {
					detail += "：" + cs.Caller
				}
				s.Nodes = append(s.Nodes, Node{
					ID: callerID, Kind: KindFile, Label: shortLabel(cs.File), Detail: detail,
				})
			}
			s.Edges = append(s.Edges, Edge{From: symID, To: callerID, Rel: RelCaller})
		}
	}
	return total
}

// appendCouplingNodes 共变节点/边：other 文件建节点（复用 file 节点），边挂
// 归属改动文件。
func appendCouplingNodes(s *Snapshot, couples []Couple) {
	for _, c := range couples {
		if c.File == "" {
			continue
		}
		id := "file:" + c.Other
		if !s.hasNode(id) {
			s.Nodes = append(s.Nodes, Node{
				ID: id, Kind: KindFile, Label: shortLabel(c.Other),
				Detail: "历史共变 " + strconv.Itoa(c.Count) + " 次",
			})
		}
		s.Edges = append(s.Edges, Edge{From: "file:" + c.File, To: id, Rel: RelCouple})
	}
}

// 容量守卫常量：artifacts_json 是 TEXT 列（MySQL 64KB 上界），超限在严格
// 模式下直接写失败——MCP/API 面会静默失去快照。节点封顶 + JSON 体积守卫
// 双保险（体积守卫兜住 Changed 清单/Notes 等 node 之外的增长）。
const (
	MaxSnapshotNodes = 150
	MaxSnapshotJSON  = 48 << 10 // 与 ArtifactDiffInput 的 48KB 落库纪律对齐
	MaxChangedList   = 500      // Changed 展示清单封顶（千文件级 PR 单清单即可顶爆体积守卫；L0 节点/风险分在封顶前已消费全量）
)

// finalize 确定化排序 + 重复边去重 + 容量守卫（节点按影响权重封顶 →
// 边随端点裁剪 → JSON 体积守卫）。任何截断记 Notes + Truncated。
func finalize(s *Snapshot) {
	slices.SortFunc(s.Nodes, func(a, b Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortStableFunc(s.Edges, func(a, b Edge) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(a.Rel, b.Rel)
	})
	dedup := s.Edges[:0]
	for i, e := range s.Edges {
		if i > 0 && e == s.Edges[i-1] {
			continue
		}
		dedup = append(dedup, e)
	}
	s.Edges = dedup

	// Changed 展示清单封顶（L0 节点/风险分已在封顶前消费全量，此处只影响
	// MCP/HTML 的清单展示）
	if len(s.Changed) > MaxChangedList {
		total := len(s.Changed)
		s.Changed = s.Changed[:MaxChangedList]
		s.Stats.Truncated = true
		s.Notes = append(s.Notes,
			"改动清单超 "+strconv.Itoa(MaxChangedList)+" 文件，展示截断 "+strconv.Itoa(total)+"→"+strconv.Itoa(MaxChangedList))
	}

	// 节点封顶（影响权重序：改动 > 包 > 测试 > 其他），边随端点裁剪——
	// 悬挂边违反快照不变量（TestNoDanglingEdges）
	if len(s.Nodes) > MaxSnapshotNodes {
		total := len(s.Nodes)
		s.Nodes = prioritizeNodes(s.Nodes, MaxSnapshotNodes)
		keep := map[string]bool{}
		for _, n := range s.Nodes {
			keep[n.ID] = true
		}
		edges := s.Edges[:0]
		for _, e := range s.Edges {
			if keep[e.From] && keep[e.To] {
				edges = append(edges, e)
			}
		}
		s.Edges = edges
		s.Stats.Truncated = true
		s.Notes = append(s.Notes,
			"影响面节点超上限，按影响权重截断 "+strconv.Itoa(total)+"→"+strconv.Itoa(MaxSnapshotNodes)+"（边随端点裁剪；完整面见交互视图/get_impact）")
	}

	// JSON 体积守卫：超 48KB 逐批降节点重排（Changed 清单是语义源不裁，
	// 由节点封顶先行兜底大 PR 场景）
	for {
		b, err := json.Marshal(s)
		if err == nil && len(b) <= MaxSnapshotJSON {
			break
		}
		if err != nil {
			break // 序列化失败（不该发生）交给上层可见的原样数据
		}
		drop := len(s.Nodes) / 5
		if drop == 0 {
			break // 只剩不可再裁的节点（ Changed 单节点），交由告警面暴露
		}
		kept := prioritizeNodes(s.Nodes, len(s.Nodes)-drop)
		keep := map[string]bool{}
		for _, n := range kept {
			keep[n.ID] = true
		}
		edges := s.Edges[:0]
		for _, e := range s.Edges {
			if keep[e.From] && keep[e.To] {
				edges = append(edges, e)
			}
		}
		s.Nodes, s.Edges = kept, edges
		s.Stats.Truncated = true
		if !containsNote(s.Notes, "体积守卫") {
			s.Notes = append(s.Notes, "快照 JSON 超 48KB 体积守卫，逐批裁节点至达标（完整面见交互视图/get_impact）")
		}
	}
	slices.Sort(s.Notes)
}

// containsNote Notes 精确子串判重（体积守卫循环内只记一次）。
func containsNote(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}

// hasNode 节点存在性（小集合线性扫，够用）。
func (s *Snapshot) hasNode(id string) bool {
	for _, n := range s.Nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// shortLabel 渲染短标签：basename，歧义时补上级目录。
func shortLabel(p string) string {
	base := filepath.Base(p)
	dir := filepath.ToSlash(filepath.Dir(p))
	if dir == "." || dir == "" {
		return base
	}
	return filepath.Base(dir) + "/" + base
}

// dedupSorted 有序去重。
func dedupSorted(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// sortedSet map 键的字典序展开（确定性遍历统一入口）。
func sortedSet(m map[string]bool) []string {
	return slices.Sorted(maps.Keys(m))
}

// sortedStrings 切片字典序副本（确定性遍历统一入口）。
func sortedStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	slices.Sort(out)
	return out
}

// countForeignPackages 受影响包中「非改动包」的数量（风险分口径）。
func countForeignPackages(pkgs []PkgImpact, changed []string) int {
	changedPkgs := map[string]bool{}
	for _, f := range changed {
		changedPkgs[filepath.ToSlash(filepath.Dir(f))] = true
	}
	n := 0
	for _, p := range pkgs {
		if !changedPkgs[p.Path] {
			n++
		}
	}
	return n
}

// depsChanged 改动是否触及依赖清单（go.mod/go.sum——传递影响超出本仓静态图）。
func depsChanged(changed []string) bool {
	for _, f := range changed {
		if f == "go.mod" || f == "go.sum" {
			return true
		}
	}
	return false
}

// testsOnlyChanged 改动是否全部为测试文件（降分口径：不达产线）。
func testsOnlyChanged(changed []string) bool {
	if len(changed) == 0 {
		return false
	}
	for _, f := range changed {
		if !strings.HasSuffix(f, "_test.go") {
			return false
		}
	}
	return true
}

// hasGoChanged 改动中是否含 Go 文件（函数级 diff 的前置条件）。
func hasGoChanged(changed []string) bool {
	for _, f := range changed {
		if strings.HasSuffix(f, ".go") {
			return true
		}
	}
	return false
}
