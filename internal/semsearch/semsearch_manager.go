package semsearch

import "context"

// indexFor 目录索引（缺则冷建并透传 aug；调用方先解锁再 Refresh——Refresh
// 自持 ix.mu）。Search/Trace/SymbolContext 三入口共用。
func (m *Manager) indexFor(dir string) *Index {
	m.mu.Lock()
	ix, ok := m.idx[dir]
	if !ok {
		ix = NewIndex(m.embedder)
		ix.aug = m.aug
		m.idx[dir] = ix
	}
	m.mu.Unlock()
	return ix
}

// Precise 目录索引是否处于精确边模式（v5.10.0：该目录索引最近一次 Refresh
// 成功应用 EdgeAugmenter 精确边；false=纯词法/augmenter 未配置/失败闩降级）。
// 目录未建索引返回 false。
func (m *Manager) Precise(dir string) bool {
	m.mu.Lock()
	ix, ok := m.idx[dir]
	m.mu.Unlock()
	if !ok {
		return false
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.precise
}

// Search 目录语义检索：增量刷新索引后按 query 向量余弦取 topK（minScore 过滤，
// ≤0 表示不过滤）。检索失败返回 error；无命中返回空切片非 error。
func (m *Manager) Search(ctx context.Context, dir, query string, topK int, minScore float32) ([]Hit, error) {
	ix := m.indexFor(dir)
	if err := ix.Refresh(ctx, dir); err != nil {
		return nil, err
	}
	return ix.Search(ctx, query, topK, minScore)
}

// Trace 两符号间最短调用链（v5.9.0，get_trace 数据面底座）：增量刷新后在符号
// 邻接图上 BFS（无向，≤graphHopsDefault 跳）。首次对未建索引目录调用会付冷建
// 索引成本（embedding 一次性，增量零）；不可达或任一端不在符号表返回 ok=false。
func (m *Manager) Trace(ctx context.Context, dir, from, to string) ([]SymRef, bool, error) {
	ix := m.indexFor(dir)
	if err := ix.Refresh(ctx, dir); err != nil {
		return nil, false, err
	}
	chain, found := ix.Path(from, to, graphHopsDefault)
	return chain, found, nil
}

// SymbolContext 单符号图上下文（v5.9.0）：定义位置 + 调用方 + 被调 + 同名候选
// 数（cands，v5.10.0；多候选合并邻接）。索引模式与 Search/Trace 相同：目录缺
// 则冷建，Refresh 失败原样上抛。
func (m *Manager) SymbolContext(ctx context.Context, dir, symbol string) (self SymRef, in, out []SymRef, cands int, ok bool, err error) {
	ix := m.indexFor(dir)
	if err = ix.Refresh(ctx, dir); err != nil {
		return
	}
	self, in, out, cands, ok = ix.SymbolContext(symbol)
	return
}
