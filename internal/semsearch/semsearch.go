// semsearch.go —— 检索主面：Manager/Index/Search 与指纹增量刷新
// （包文档见 doc.go；图面见 graph.go/graph_query.go，抽取面见 chunks.go）。

package semsearch

import (
	"context"
	"sync"
)

// Embedder 文本向量化接口（*rag.OpenAIEmbedder 天然满足）。
type Embedder interface {
	// Embed 批量向量化；返回向量数须与输入数一致。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Dim 向量维度。
	Dim() int
}

// Chunk 检索片段：一段供 embedding 的结构化摘要 + 命中展示元数据。
type Chunk struct {
	Path   string // 仓库相对路径
	Line   int    // 起始行（1-based）
	Symbol string // Go 顶层声明节点键（NodeKey 契约；文件级 chunk 为空）
	Text   string // embedding 输入（path:line + 源码片段）
}

// Hit 单条语义命中。
type Hit struct {
	Path      string   `json:"path"`
	Line      int      `json:"line"`
	Symbol    string   `json:"symbol,omitempty"`
	Score     float32  `json:"score"`
	Text      string   `json:"text"`
	Neighbors []SymRef `json:"neighbors,omitempty"` // 图邻接（v5.9.0，Symbol 非空时自动附）
}

// fileIndex 单文件的 chunk、调用边与向量（指纹不变即整文件复用，增量刷新的粒度）。
type fileIndex struct {
	chunks []Chunk
	edges  []symEdge // 调用边（v5.9.0，与 chunks 同一次 AST 解析的副产品）
	vecs   [][]float32
	fp     fileFP
}

// fileFP 文件指纹（size+mtime；变更即整文件重抽重嵌，增量刷新的判定粒度）。
type fileFP struct {
	size  int64
	mtime int64
}

// Index 一个工作副本目录的语义索引（进程内存驻留；工作副本生命周期由池侧
// TTL 管理，本包不做目录清理）。embedder 为 nil 即纯图模式（v5.11.0）：图面
// 可用，Search 拦截报错。
type Index struct {
	embedder Embedder

	mu             sync.Mutex            // 护 files/paths/sym/aug*/precise/truncated（Refresh 与 Search 并发）
	files          map[string]*fileIndex // key: 仓库相对路径
	paths          []string              // 排序路径快照（Search 确定序遍历）
	sym            *symGraph             // 符号邻接图（Refresh 收尾重建，nil=未构建；v5.9.0 图面）
	maxFiles       int                   // 文件数上限（默认 maxDefaultFiles，测试可调低）
	maxTotalChunks int                   // 全库 chunk 上限（embedding 成本闸）
	truncated      bool                  // 触达任一上限即置位（调用方可透出告警）

	// 精确边钩子（v5.10.0）：aug 由 Manager 经 WithEdges 注入（直建 Index 留空
	// =纯词法）；augSig/augEdges 是成功应用的签名与边缓存；augFailed 失败闩——
	// 本 Index 生命周期内不再重试（工作副本目录 per head-SHA，新 MR 新 Index
	// 自动重试）；precise=最近一次 Refresh 成功应用精确边（Manager.Precise 透出）。
	aug       EdgeAugmenter
	augSig    uint64
	augEdges  [][2]string
	augFailed bool
	precise   bool
}

// NewIndex 构建空索引（Refresh 前检索返回空）。e 允许 nil（v5.11.0 纯图模式）：
// Refresh 照常扫文件抽 chunk/边/建图，仅跳过向量化（vecs 置 nil）；Search
// （含 Manager.Search——经 Index.Search 到达同一拦截，单源）返回「语义检索
// 未启用」error，图查询 Path/SymbolContext/Size/Truncated 不受影响。
func NewIndex(e Embedder) *Index {
	return &Index{
		embedder:       e,
		files:          map[string]*fileIndex{},
		maxFiles:       maxDefaultFiles,
		maxTotalChunks: maxDefaultTotalChunks,
	}
}

// Manager 按目录缓存的索引管理器（search_sem 数据面入口）：目录首见时构建
// 索引，此后每次检索前按文件指纹增量刷新——只重嵌变更文件的 chunk。
type Manager struct {
	embedder Embedder
	aug      EdgeAugmenter // 精确边注入钩子（WithEdges 注入，透传给所建 Index）
	mu       sync.Mutex
	idx      map[string]*Index
}

// Option Manager 构建选项（装配侧按需注入可选能力）。
type Option func(*Manager)

// WithEdges 注入精确边 augmenter（v5.10.0 档一钩子，档二实现方注入）：所建
// Index 的 Refresh 尾部按文件集签名增量拉取精确调用边，成功应用即替换词法
// 邻接（Precise(dir)=true）；失败降级词法并置失败闩不再重试。
func WithEdges(a EdgeAugmenter) Option {
	return func(m *Manager) { m.aug = a }
}

// NewManager 构建管理器（opts 按序应用）。e 允许 nil（v5.11.0）=纯图 Manager：
// 所建 Index 同为纯图模式（图查询可用、Search 报错）。
func NewManager(e Embedder, opts ...Option) *Manager {
	m := &Manager{embedder: e, idx: map[string]*Index{}}
	for _, o := range opts {
		o(m)
	}
	return m
}
