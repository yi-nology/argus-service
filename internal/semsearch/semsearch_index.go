package semsearch

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
)

// Size 索引内 chunk 总数（观测面）。
func (ix *Index) Size() int {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	n := 0
	for _, fi := range ix.files {
		n += len(fi.chunks)
	}
	return n
}

// Truncated 建索引时是否触达文件数/chunk 数上限。
func (ix *Index) Truncated() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.truncated
}

// Precise 索引是否处于精确边模式（v5.11.0 导出直建 Index 的读面；Manager.Precise
// 是同判的目录入口）：最近一次 Refresh 成功应用 EdgeAugmenter 精确边为 true；
// false=纯词法/augmenter 未配置/失败闩降级。图工具据此注明「词法级边」或
// 「类型解析级边（go/types）」。
func (ix *Index) Precise() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.precise
}

// Searchable 索引是否可语义检索（v6.1.0）：embedder 非 nil 即 true——纯图模式
// （NewIndex(nil)）图面可用但 Search 在单源拦截报错，消费方（审查管线挂
// search_sem 前的挂载判定）据此避免挂出必然报错的死工具。与 Index.Search 的
// embedder 判断同源（embedder 构造期定型，读锁防御未来可能的运行期注入）。
func (ix *Index) Searchable() bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.embedder != nil
}

// Refresh 扫描目录并增量刷新索引：新文件/指纹变更的文件重新抽 chunk 并批量
// 向量化，消失的文件整组剔除。单文件扫描失败跳过（与 codesearch 同宽容度）；
// embedding 失败原样上抛（调用方决定降级文案）。embedder 为 nil（纯图模式）
// 时跳过向量化——chunk/边照常落地（vecs 置 nil），图面与 Size/Truncated 语义
// 不变。收尾顺带跑精确边钩子（applyAugLocked：签名变了才拉边，成功应用即
// 精确模式）。
func (ix *Index) Refresh(ctx context.Context, dir string) error {
	found, modPath, scanned, err := ix.scanRefresh(dir)
	if err != nil {
		return err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.truncated = ix.truncated || scanned > ix.maxFiles

	live, todo := ix.collectPendingLocked(found, modPath)
	ix.pruneDeletedLocked(live)
	return ix.storeAndRebuildLocked(ctx, dir, found, todo)
}

// refreshPending Refresh 待落地的单文件抽取结果（收集与落地两段的中转项）。
type refreshPending struct {
	rel    string
	fp     fileFP
	chunks []Chunk
	edges  []symEdge // 与 chunks 同一次解析的调用边（v5.9.0 图面）
}

// scanRefresh Refresh 前置扫描：目录枚举 + go.mod module 声明（import 限定消歧
// 输入；缺失空串降级）+ 文件数上限截断（truncated 位在取锁后登记）。
func (ix *Index) scanRefresh(dir string) (found []scannedFile, modPath string, scanned int, err error) {
	found, err = scanDir(dir)
	if err != nil {
		return nil, "", 0, err
	}
	modPath = modulePathOf(dir)
	scanned = len(found)
	if scanned > ix.maxFiles {
		found = found[:ix.maxFiles]
	}
	return found, modPath, scanned, nil
}

// collectPendingLocked 抽取本轮待重嵌文件（调用方持锁）：全量先标 live（预算
// 触顶的文件仍在盘上，只是本轮不嵌——先标后算，杜绝「预算 break 提前退出把
// 未处理文件误当已删除剔除」），再按指纹/chunk 预算收 todo。
func (ix *Index) collectPendingLocked(found []scannedFile, modPath string) (map[string]bool, []refreshPending) {
	live := make(map[string]bool, len(found))
	for _, f := range found {
		live[f.rel] = true
	}
	var todo []refreshPending
	total := 0
	for _, fi := range ix.files {
		total += len(fi.chunks)
	}
	for _, f := range found {
		if old, ok := ix.files[f.rel]; ok && old.fp == f.fp {
			continue
		}
		chunks, edges := extractChunks(f.rel, f.content, modPath)
		if len(chunks) > maxFileChunks {
			chunks = chunks[:maxFileChunks]
		}
		budget := ix.maxTotalChunks - total
		if budget <= 0 {
			ix.truncated = true
			continue // 本轮不嵌；live 已标，不会误删既有条目
		}
		if len(chunks) > budget {
			chunks = chunks[:budget]
			ix.truncated = true
		}
		total += len(chunks)
		todo = append(todo, refreshPending{rel: f.rel, fp: f.fp, chunks: chunks, edges: edges})
	}
	return live, todo
}

// pruneDeletedLocked 剔除本轮扫描中已消失的文件整组条目（调用方持锁）。
func (ix *Index) pruneDeletedLocked(live map[string]bool) {
	for rel := range ix.files {
		if !live[rel] {
			delete(ix.files, rel)
		}
	}
}

// storeAndRebuildLocked 落地与重建（调用方持锁）：路径快照/精确边钩子/图面
// 三件先重建一次（删除与预算截断同样要反映），todo 空即止；否则按 embedder
// 有无分流——纯图模式直接落 chunk，向量化模式批量 Embed 后落 chunk+向量，
// 两种落地都再重建一次三件套。
func (ix *Index) storeAndRebuildLocked(ctx context.Context, dir string,
	found []scannedFile, todo []refreshPending) error {
	ix.rebuildPathsLocked()            // 删除/预算截断都可能改变文件集，快照恒重建（此前仅在重嵌路径重建，todo 空时删除残留）
	ix.applyAugLocked(ctx, dir, found) // v5.10.0：精确边钩子（签名门控+失败闩，见 graph.go）
	ix.rebuildGraphLocked()            // v5.9.0：图面与符号集同源，删除/预算截断同样要重建
	if len(todo) == 0 {
		return nil
	}
	// 纯图模式（v5.11.0）：embedder 未注入——跳过向量化，chunk/边照常落地
	//（vecs 置 nil），图随 rebuildGraphLocked 照建；Search 面在 Index.Search
	// 单源拦截（Manager.Search 复用该判断）。
	if ix.embedder == nil {
		for _, p := range todo {
			ix.files[p.rel] = &fileIndex{fp: p.fp, chunks: p.chunks, edges: p.edges}
		}
		ix.rebuildPathsLocked()
		ix.applyAugLocked(ctx, dir, found)
		ix.rebuildGraphLocked()
		return nil
	}
	// 批量向量化（一次 Embed 调用摊薄 HTTP 往返）。
	var texts []string
	for _, p := range todo {
		for _, c := range p.chunks {
			texts = append(texts, c.Text)
		}
	}
	vecs, err := ix.embedder.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("chunk 向量化失败（%d 段）: %w", len(texts), err)
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("向量化返回数不符: got %d, want %d", len(vecs), len(texts))
	}
	pos := 0
	for _, p := range todo {
		fi := &fileIndex{fp: p.fp, edges: p.edges}
		for _, c := range p.chunks {
			fi.chunks = append(fi.chunks, c)
			fi.vecs = append(fi.vecs, vecs[pos])
			pos++
		}
		ix.files[p.rel] = fi
	}
	ix.rebuildPathsLocked()
	ix.applyAugLocked(ctx, dir, found) // v5.10.0：签名未变即零成本沿用既有精确边
	ix.rebuildGraphLocked()            // v5.9.0：重嵌落地后按新符号集重建图面
	return nil
}

// rebuildPathsLocked 重建排序路径快照（调用方持锁）。
func (ix *Index) rebuildPathsLocked() {
	ix.paths = ix.paths[:0]
	for rel := range ix.files {
		ix.paths = append(ix.paths, rel)
	}
	slices.Sort(ix.paths)
}

// Search 按 query 向量对全库 chunk 余弦打分，取相似度降序 topK（minScore>0 时
// 过滤低分命中）。纯图模式（embedder nil，v5.11.0）返回「语义检索未启用」
// error——Manager.Search 经本方法到达同一判断，拦截单源在此（Manager 层不重复）。
func (ix *Index) Search(ctx context.Context, query string, topK int, minScore float32) ([]Hit, error) {
	if ix.embedder == nil {
		return nil, fmt.Errorf("语义检索未启用（未配置 embedding）")
	}
	qv, err := ix.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("query 向量化失败: %w", err)
	}
	if len(qv) != 1 || len(qv[0]) == 0 {
		return nil, fmt.Errorf("query 向量化返回空")
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	type scored struct {
		h Hit
	}
	hits := make([]scored, 0, 64)
	for _, rel := range ix.paths {
		fi := ix.files[rel]
		if fi == nil {
			continue // 快照与文件集的瞬时不一致防御
		}
		for i, c := range fi.chunks {
			s := cosine(qv[0], fi.vecs[i])
			if minScore > 0 && s < minScore {
				continue
			}
			hits = append(hits, scored{h: Hit{
				Path: c.Path, Line: c.Line, Symbol: c.Symbol, Score: s, Text: c.Text,
			}})
		}
	}
	slices.SortFunc(hits, func(a, b scored) int { return cmp.Compare(b.h.Score, a.h.Score) })
	if topK <= 0 {
		topK = 5
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		// v5.9.0 图面：Symbol 非空的命中附调用方/被调邻接（sym 为 nil 跳过——
		// 图随 Refresh 重建，Refresh 成功后不会为 nil，防御留底）。
		if h.h.Symbol != "" && ix.sym != nil {
			h.h.Neighbors = ix.sym.neighborsOf(h.h.Symbol)
		}
		out = append(out, h.h)
	}
	return out, nil
}

// cosine 余弦相似度（零向量回 0；f64 中转避免 f32 累加误差）。
func cosine(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := range n {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}
