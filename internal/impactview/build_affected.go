// build_affected.go —— L1/L3 受影响面包闭包：importers 索引 / BFS 闭包 / 包与测试节点落图。

package impactview

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// affectedRow 受影响包闭包的一行（包聚合 + 来路信息，边渲染用）。
type affectedRow struct {
	pi            PkgImpact
	parent        string
	parentChanged bool
}

// buildAffectedPackages L1 反向闭包：改动文件 → 改动包 → 谁 import 了它们
// （BFS ≤ MaxImportDepth，封顶 MaxPackages）。节点/边/包聚合一并写入 s，
// 返回包聚合切片（风险分消费）。
func buildAffectedPackages(s *Snapshot, ws *goMods, changed []string) []PkgImpact {
	if ws == nil || ws.module == "" {
		return nil
	}
	changedPkgs := map[string]bool{}
	for _, f := range changed {
		changedPkgs[filepath.ToSlash(filepath.Dir(f))] = true
	}
	importers := buildImporterIndex(ws)
	rows, seen, truncated := affectedClosure(ws, importers, changedPkgs)
	if truncated {
		s.Stats.Truncated = true
		s.Notes = append(s.Notes, "受影响包闭包超上限，深层依赖未展开")
	}
	writeAffectedGraph(s, rows, seen, importers, changed)
	return s.Packages
}

// buildImporterIndex 被依赖目录 → 依赖方目录（importers，字典序确定化）。
func buildImporterIndex(ws *goMods) map[string][]string {
	importers := map[string][]string{}
	for _, d := range ws.dirs {
		for _, imp := range ws.imports[d] {
			importers[imp] = append(importers[imp], d)
		}
	}
	for d := range importers {
		slices.Sort(importers[d])
	}
	return importers
}

// affectedClosure BFS 展开受影响包闭包（≤ MaxImportDepth / 封顶 MaxPackages）；
// parent 记来路包、parentChanged 标记来路是改动包。返回聚合行、已访问集
// （暴露面判定用）与是否触顶截断。
func affectedClosure(ws *goMods, importers map[string][]string, changedPkgs map[string]bool) ([]affectedRow, map[string]bool, bool) {
	type qe struct {
		pkg           string
		depth         int
		parent        string
		parentChanged bool
	}
	queue := make([]qe, 0, len(changedPkgs))
	for _, p := range sortedSet(changedPkgs) {
		queue = append(queue, qe{pkg: p, depth: 0})
	}
	seen := map[string]bool{}
	var rows []affectedRow
	for len(queue) > 0 && len(rows) < MaxPackages {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur.pkg] {
			continue
		}
		seen[cur.pkg] = true
		rows = append(rows, affectedRow{
			pi: PkgImpact{
				Path:   cur.pkg,
				Files:  ws.fileCount[cur.pkg],
				Tests:  ws.testFiles[cur.pkg],
				Depth:  cur.depth,
				Direct: cur.depth == 0,
			},
			parent:        cur.parent,
			parentChanged: cur.parentChanged,
		})
		for _, nx := range sortedStrings(importers[cur.pkg]) {
			if !seen[nx] && cur.depth < MaxImportDepth {
				queue = append(queue, qe{pkg: nx, depth: cur.depth + 1,
					parent: cur.pkg, parentChanged: cur.depth == 0})
			}
		}
	}
	return rows, seen, len(queue) > 0
}

// writeAffectedGraph 闭包结果落图：暴露面标记 → 跳数/路径排序 → 包节点/边
// （改动包不自建 pkg: 节点）→ L3 测试面节点/边。
func writeAffectedGraph(s *Snapshot, rows []affectedRow, seen map[string]bool, importers map[string][]string, changed []string) {
	// 对外暴露面：被受影响闭包之外的包 import（出口风险）
	for i := range rows {
		for _, imp := range importers[rows[i].pi.Path] {
			if !seen[imp] {
				rows[i].pi.Exposed = true
				break
			}
		}
	}
	// 排序展示序：先跳数后路径
	slices.SortFunc(rows, func(a, b affectedRow) int {
		if c := cmp.Compare(a.pi.Depth, b.pi.Depth); c != 0 {
			return c
		}
		return strings.Compare(a.pi.Path, b.pi.Path)
	})

	for _, r := range rows {
		s.Packages = append(s.Packages, r.pi)
		if r.pi.Direct {
			continue // 改动包不自建 pkg: 节点（file 节点已表达）
		}
		id := "pkg:" + r.pi.Path
		s.Nodes = append(s.Nodes, Node{
			ID: id, Kind: KindPackage, Label: pkgLabel(r.pi.Path),
			Detail: "依赖跳数 " + strconv.Itoa(r.pi.Depth),
		})
		from := "pkg:" + r.parent
		if r.parentChanged {
			// 改动包无 pkg: 节点，边改挂其首个改动文件（ID 带 file: 前缀）
			from = "file:" + firstChangedFileIn(r.parent, changed)
		}
		if from != "" {
			s.Edges = append(s.Edges, Edge{From: from, To: id, Rel: RelImport})
		}
	}
	// L3 测试面：受影响包的测试节点（包级聚合，不逐文件展开）
	for _, p := range s.Packages {
		if p.Tests == 0 {
			continue
		}
		id := "test:" + p.Path
		s.Nodes = append(s.Nodes, Node{
			ID: id, Kind: KindTest, Label: "tests " + pkgLabel(p.Path),
			Detail: strconv.Itoa(p.Tests) + " 个测试文件",
		})
		src := "pkg:" + p.Path
		if p.Direct {
			src = "file:" + firstChangedFileIn(p.Path, changed)
		}
		if src != "" {
			s.Edges = append(s.Edges, Edge{From: src, To: id, Rel: RelTestOf})
		}
	}
}

// pkgLabel 包节点展示标签（module 根目录 "." 不可读，收敛为显式标记）。
func pkgLabel(path string) string {
	if path == "." {
		return "(module 根)"
	}
	return path
}

// firstChangedFileIn 目录内首个改动文件（边端点兜底；无命中返回空）。
func firstChangedFileIn(pkgDir string, changed []string) string {
	for _, f := range changed {
		if filepath.ToSlash(filepath.Dir(f)) == pkgDir {
			return f
		}
	}
	return ""
}
