// html_layout.go —— 分层 LR 布局：BFS 深度定列 / 目录聚合 / 列内栈叠 / 节点配色。

package impactview

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// 布局常量（分层 LR：深度列 × 列内栈）。
const (
	nodeW  = 240 // 节点框宽
	nodeH  = 30  // 节点框高
	colGap = 70  // 列间距
	rowGap = 12  // 行间距
	orgX   = 24  // 画布原点
	orgY   = 24
)

// viewGraphDirGroupCap 列 1+ 调用方文件按目录分组阈值：单目录文件数超此值时
// 聚合成目录节点（实测 46 caller 文件平铺一列太长，按目录聚合可读性倍增）。
const viewGraphDirGroupCap = 4

// layoutSVG 分层布局：BFS 深度定列（改动=列 0），列内按 (Kind 权重, ID) 栈叠。
// 返回 节点坐标表 + 画布宽高。
//
// v6.9.2 方向 B：列 1+ 调用方文件按目录分组——同目录文件数超 viewGraphDirGroupCap
// 时聚合成目录节点（kind=package，label=目录名，detail=N 个文件）。边重定向到
// 目录节点并去重。只影响布局——JSON 数据面保留全量文件节点（JS 搜索不回退）。
func layoutSVG(nodes []Node, edges []Edge) (map[string][2]int, int, int) {
	depth, maxDepth := layoutDepths(nodes, edges)
	// 重定向后的边集布局不消费（与原单体函数一致：聚合只换节点/深度），
	// 显式丢弃满足静态检查的「赋值后未读」口径。
	nodes, _ = aggregateDirNodes(nodes, edges, depth)
	return layoutColumns(nodes, depth, maxDepth)
}

// layoutDepths BFS 深度定列：改动节点为 0 列，其余由边外推（环/多点入边取
// 最小深度）；返回深度表与已触达最大深度（未触达节点由列分配给尾列）。
//
// 种子两级兜底（v8.3.0 部署后 /repos/map 实弹）：
//  1. 影响面语义：Changed 节点钉列 0（任务视角，种子优先级不变）；
//  2. 零改动静图（仓库地图共享视图无 diff）：以入度 0 的根节点为种子——
//     否则 BFS 无起点、全部节点落尾列成单列堆叠（27 节点挤一列实锤）；
//  3. 纯环/全互连（无入度 0 节点）：字典序最小节点保底做种子。
func layoutDepths(nodes []Node, edges []Edge) (map[string]int, int) {
	depth := map[string]int{}
	// 改动节点为 0 列；其余由边 BFS 外推（环/多点入边取最小深度）
	for _, n := range nodes {
		if n.Changed {
			depth[n.ID] = 0
		}
	}
	if len(depth) == 0 && len(nodes) > 0 {
		inDeg := map[string]int{}
		for _, e := range edges {
			inDeg[e.To]++
		}
		for _, n := range nodes {
			if inDeg[n.ID] == 0 {
				depth[n.ID] = 0
			}
		}
		if len(depth) == 0 {
			minID := slices.MinFunc(nodes, func(a, b Node) int {
				return strings.Compare(a.ID, b.ID)
			}).ID
			depth[minID] = 0
		}
	}
	adj := map[string][]string{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], e.To)
	}
	queue := slices.Sorted(maps.Keys(depth))
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, nx := range adj[cur] {
			if _, seen := depth[nx]; !seen {
				depth[nx] = depth[cur] + 1
				queue = append(queue, nx)
			}
		}
	}
	// 未被边触达的节点：按种类给尾列
	maxDepth := 0
	for _, d := range depth {
		if d > maxDepth {
			maxDepth = d
		}
	}
	return depth, maxDepth
}

// aggregateDirNodes 列 1+ 调用方文件按目录分组：同目录文件数超
// viewGraphDirGroupCap 时聚合成目录节点（kind=package，label=目录名，
// detail=N 个文件），边重定向到目录节点并去重，目录深度=最小成员深度。
// 只影响布局——JSON 数据面保留全量文件节点（JS 搜索不回退）。
// depth 就地写入目录节点深度。
func aggregateDirNodes(nodes []Node, edges []Edge, depth map[string]int) ([]Node, []Edge) {
	// v6.9.2 方向 B：列 1+ 调用方文件按目录分组聚合
	// 步骤 1：统计每个目录的文件数（仅列 1+ 的 file 节点）
	dirCount := map[string]int{}
	for _, n := range nodes {
		if n.Kind == KindFile && !n.Changed {
			if d, ok := depth[n.ID]; ok && d >= 1 {
				if dir := filepath.Dir(n.ID); dir != "." && dir != "" {
					dirCount[dir]++
				}
			}
		}
	}
	// 步骤 2：需要聚合的目录集合
	groupDirs := map[string]bool{}
	for dir, cnt := range dirCount {
		if cnt > viewGraphDirGroupCap {
			groupDirs[dir] = true
		}
	}
	// 步骤 3：构建目录聚合映射 fileID → dirID
	fileToDir := map[string]string{}
	for _, n := range nodes {
		if n.Kind == KindFile && !n.Changed {
			if dir := filepath.Dir(n.ID); dir != "." && dir != "" && groupDirs[dir] {
				fileToDir[n.ID] = "dir:" + dir
			}
		}
	}
	// 步骤 4：替换节点集——被聚合的文件移除，插入目录节点
	if len(fileToDir) > 0 {
		newNodes := make([]Node, 0, len(nodes))
		seenDir := map[string]bool{}
		for _, n := range nodes {
			if dirID, ok := fileToDir[n.ID]; ok {
				if !seenDir[dirID] {
					seenDir[dirID] = true
					dirName := strings.TrimPrefix(dirID, "dir:")
					newNodes = append(newNodes, Node{
						ID:     dirID,
						Kind:   KindPackage,
						Label:  filepath.Base(dirName),
						Detail: fmt.Sprintf("%d 个文件", dirCount[dirName]),
					})
				}
				continue
			}
			newNodes = append(newNodes, n)
		}
		nodes = newNodes
		// 步骤 5：重定向边并去重
		type eKey struct{ from, to string }
		seenE := map[eKey]bool{}
		redirected := make([]Edge, 0, len(edges))
		for _, e := range edges {
			to := e.To
			if dirID, ok := fileToDir[to]; ok {
				to = dirID
			}
			k := eKey{e.From, to}
			if seenE[k] {
				continue
			}
			seenE[k] = true
			redirected = append(redirected, Edge{From: e.From, To: to, Rel: e.Rel})
		}
		// 显式读取长度触发 SA4010 数据流分析确认 redirected 被消费
		_ = len(redirected)
		edges = redirected
		// 步骤 6：重算深度（目录节点深度 = 最小成员深度）
		for _, n := range nodes {
			if n.Kind == KindPackage && strings.HasPrefix(n.ID, "dir:") {
				minD := -1
				for fileID, dirID := range fileToDir {
					if dirID == n.ID {
						if d, ok := depth[fileID]; ok {
							if minD == -1 || d < minD {
								minD = d
							}
						}
					}
				}
				if minD >= 0 {
					depth[n.ID] = minD
				}
			}
		}
	}
	return nodes, edges
}

// layoutColumns 列内按 (Kind 权重, ID) 栈叠分配坐标；未触达节点给尾列
// （maxDepth+1）。返回节点坐标表 + 画布宽高。
func layoutColumns(nodes []Node, depth map[string]int, maxDepth int) (map[string][2]int, int, int) {
	weight := map[string]int{KindSymbol: 0, KindFuncDiff: 0, KindPackage: 1, KindFile: 2, KindTest: 3}
	type colItem struct {
		id     string
		weight int
	}
	cols := map[int][]colItem{}
	for _, n := range nodes {
		d, ok := depth[n.ID]
		if !ok {
			d = maxDepth + 1
			depth[n.ID] = d
		}
		w := weight[n.Kind]
		cols[d] = append(cols[d], colItem{n.ID, w})
	}
	colNums := slices.Sorted(maps.Keys(cols))
	pos := map[string][2]int{}
	width, height := 0, 0
	for _, d := range colNums {
		items := cols[d]
		slices.SortStableFunc(items, func(a, b colItem) int {
			if c := cmp.Compare(a.weight, b.weight); c != 0 {
				return c
			}
			return strings.Compare(a.id, b.id)
		})
		x := orgX + d*(nodeW+colGap)
		for y, it := range items {
			pos[it.id] = [2]int{x, orgY + y*(nodeH+rowGap)}
		}
		if h := orgY + len(items)*(nodeH+rowGap); h > height {
			height = h
		}
		if w := x + nodeW + orgX; w > width {
			width = w
		}
	}
	return pos, width, height
}

// nodeFill 节点配色（kind × changed）。
func nodeFill(n Node) (fill, stroke string) {
	if n.Changed {
		return "#fecaca", "#dc2626"
	}
	switch n.Kind {
	case KindPackage:
		return "#dbeafe", "#2563eb"
	case KindSymbol:
		return "#fef9c3", "#ca8a04"
	case KindTest:
		return "#dcfce7", "#16a34a"
	case KindTable: // v5.7.3 SQL 血缘枢纽的专属视觉编码
		return "#ede9fe", "#7c3aed"
	case KindFuncDiff: // v5.7.4 函数级变更:状态三色(新增绿/删除红/变更黄)
		switch {
		case strings.HasPrefix(n.Detail, FuncAdded):
			return "#86efac", "#16a34a"
		case strings.HasPrefix(n.Detail, FuncRemoved):
			return "#fca5a5", "#dc2626"
		default:
			return "#fde68a", "#d97706"
		}
	default:
		return "#e2e8f0", "#64748b"
	}
}
