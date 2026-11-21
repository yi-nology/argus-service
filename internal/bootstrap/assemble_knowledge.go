package bootstrap

import (
	"context"
	"fmt"
	"github.com/oekit/pkg/httpjson"
	"os"
	"path/filepath"
	"strings"
	"time"

	amsqlite "github.com/yi-nology/agent-memory/storage/sqlite"

	"github.com/oagentkit/rag"
	"github.com/oekit/logx"
	"github.com/yi-nology/agent-memory/ports"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/goedges"
	"github.com/yi-nology/argus/internal/knowledgefilter"
	"github.com/yi-nology/argus/internal/memorykernel"
	"github.com/yi-nology/argus/internal/semsearch"
)

// buildSemSearchAndMemKernel 语义检索底座（v3.126.0）：ARGUS_SEMSEARCH +
// ARGUS_EMBEDDING_* 双门——embedding 与知识库共用一套配置；未就绪留 nil，
// search_sem 运行期报未启用。精确调用边档（v5.10.0）：ARGUS_GRAPH_PRECISE
// 开启时注入 go/types 精确边抽取器（goedges，EdgeAugmenter 档二实现）——
// 工作副本不可编译时 semsearch 侧失败闩自动降级词法边，装配侧无感。
// 以及评审记忆内核（v3.151.0，ARGUS_MEMORY_KERNEL opt-in）：agent-memory
// 底座——落库前强制脱敏 + 召回注入防伪（MemoryBand）+ 事实证据链闭合。
// 打开失败降级无记忆面（记忆是增强面，不许拖垮启动）；库文件固定
// <MemoryDir>/kernel.db。v5.0.0 文件面退役：内核是记忆唯一路径。
// 语义召回轨（v5.0.0）：embedding 端点已配即接通（与 semsearch 同一套
// ARGUS_EMBEDDING_* 配置；sqlite 内置持久向量索引 + RRF 词面融合），
// 失败运行期降级词面。批量口→单文本口适配在此做，memorykernel 层保持纯净。
// embeddingRetry 嵌入端点重试选项（生产实锤：网关 503「限流服务暂不可用」
// 一次就把 4000 段向量化整批打挂，图谱降级纯词面）。rag 默认单次尝试（成本
// 决策交给调用方）——argus 侧按 3 次指数退避接线：5xx/429/网络可重试、
// 4xx 参数错不空转（httpjson 默认判定）。
func embeddingRetry() rag.EmbedderOption {
	return rag.WithRetry(httpjson.RetryConfig{
		MaxAttempts: 3,
		BaseDelay:   500 * time.Millisecond,
		Jitter:      100 * time.Millisecond,
	})
}

func buildSemSearchAndMemKernel(cfg *config.Config, log logx.Logger, sys *System) *semsearch.Manager {
	var semMgr *semsearch.Manager
	if cfg.SemSearch && cfg.EmbeddingBaseURL != "" {
		var semOpts []semsearch.Option
		if cfg.GraphPrecise {
			semOpts = append(semOpts, semsearch.WithEdges(goedges.New()))
		}
		semMgr = semsearch.NewManager(semsearch.NewByteBatchEmbedder(rag.NewOpenAIEmbedder(
			cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim,
			embeddingRetry())),
			semOpts...)
		log.Info("argus.boot.semsearch_enabled", "embedding", cfg.EmbeddingModel, "dim", cfg.EmbeddingDim,
			"precise", cfg.GraphPrecise)
	} else if cfg.GraphPrecise {
		log.Warn("argus.boot.graph_precise_unused",
			"reason", "ARGUS_SEMSEARCH/ARGUS_EMBEDDING_BASE_URL 未就绪——精确边无消费面")
	}
	if cfg.MemoryKernel && cfg.MemoryDir != "" {
		kpath := filepath.Join(cfg.MemoryDir, "kernel.db")
		var kemb ports.Embedder
		if cfg.EmbeddingBaseURL != "" {
			kemb = singleTextEmbedder{rag.NewOpenAIEmbedder(
				cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim,
				embeddingRetry())}
		}
		// FTS 分词器（v5.5.1，am#15）：缺省 trigram——unicode61 不切 CJK，中文
		// 连续文本整段成 token，词面召回轨失效。存量库切分词器需重建 FTS 虚表。
		kopts := []amsqlite.Option{amsqlite.WithFTSTokenize(cfg.MemoryFTSTokenize)}
		if k, err := memorykernel.Open(kpath, kemb, log, kopts...); err != nil {
			log.Warn("argus.boot.memorykernel_failed", "path", kpath, "error", err.Error())
		} else {
			sys.MemKernel = k
			log.Info("argus.boot.memorykernel_enabled", "path", kpath, "semantic", kemb != nil,
				"fts_tokenize", cfg.MemoryFTSTokenize)
		}
	}
	return semMgr
}

// buildKnowledge 知识检索装配（v3.119.0 Hybrid 双路优先，agentkit v0.14.0）：
//   - Milvus + 本地知识目录双备 → NewHybrid（语义+TF-IDF RRF 融合，检索单路
//     失败自动降级另一路——此前 Milvus 查询故障即检索全挂）；
//   - 仅 Milvus → Milvus（构建失败降级本地）；仅本地 → Local；全无 → Noop。
func buildKnowledge(ctx context.Context, cfg *config.Config, log logx.Logger) rag.KnowledgeService {
	local, localOK := buildLocalRAG(cfg.KnowledgeDir, log)
	var milvus rag.KnowledgeService
	if cfg.MilvusAddr != "" && cfg.EmbeddingBaseURL != "" {
		embedder := rag.NewOpenAIEmbedder(cfg.EmbeddingBaseURL, cfg.EmbeddingAPIKey, cfg.EmbeddingModel, cfg.EmbeddingDim,
			embeddingRetry())
		milvusCfg := rag.MilvusConfig{
			Address:    cfg.MilvusAddr,
			Collection: cfg.MilvusCollection,
			Dimension:  cfg.EmbeddingDim,
		}
		if k, e := rag.NewMilvusStore(ctx, milvusCfg, embedder); e == nil {
			milvus = k
			log.Info("argus.boot.milvus_ready", "addr", cfg.MilvusAddr, "collection", cfg.MilvusCollection,
				"embedding", cfg.EmbeddingModel, "dim", cfg.EmbeddingDim)
			// 启动时索引 knowledge 目录到 Milvus（语义路语料）
			if fi, err := os.Stat(cfg.KnowledgeDir); err == nil && fi.IsDir() {
				indexKnowledgeDir(ctx, k, cfg.KnowledgeDir, log)
			}
		} else {
			log.Warn("argus.boot.milvus_failed", "addr", cfg.MilvusAddr, "error", e.Error(),
				"fallback", "本地 RAG")
		}
	}
	// 租户过滤装饰器（#84/#92）：出口单点包层——search_knowledge/R3 工具/R4
	// 预检索全部消费过滤后检索（超取+宿主侧按租户裁剪），写侧 Index 直通。
	switch {
	case milvus != nil && localOK:
		log.Info("argus.boot.knowledge_hybrid", "semantic", "milvus", "keyword", cfg.KnowledgeDir)
		return knowledgefilter.Wrap(rag.NewHybrid(milvus, local))
	case milvus != nil:
		return knowledgefilter.Wrap(milvus)
	case localOK:
		return knowledgefilter.Wrap(local)
	default:
		return rag.NewNoop()
	}
}

// buildLocalRAG 本地 RAG（目录缺失/构建失败返回 nil 并日志留痕）。
func buildLocalRAG(dir string, log logx.Logger) (*rag.Local, bool) {
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		log.Warn("argus.boot.knowledge_absent", "dir", dir,
			"hint", "创建目录并放入 markdown 知识文件即启用 search_knowledge")
		return nil, false
	}
	k, e := rag.NewLocal(dir)
	if e != nil {
		log.Warn("argus.boot.knowledge_failed", "dir", dir, "error", e.Error())
		return nil, false
	}
	log.Info("argus.boot.knowledge_ready", "dir", dir)
	return k, true
}

// indexKnowledgeDir 将知识库目录中的 markdown 文件索引到 Milvus。
func indexKnowledgeDir(ctx context.Context, store *rag.MilvusStore, dir string, log logx.Logger) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warn("argus.boot.index_dir_failed", "dir", dir, "error", err.Error())
		return
	}
	indexed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Warn("argus.boot.index_file_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		if err := store.Index(ctx, entry.Name(), string(data)); err != nil {
			log.Warn("argus.boot.index_failed", "file", entry.Name(), "error", err.Error())
			continue
		}
		indexed++
	}
	log.Info("argus.boot.knowledge_indexed", "dir", dir, "files", indexed)
}

// singleTextEmbedder rag 批量 Embedder → agent-memory 单文本口的装配侧适配件
// （v5.0.0 记忆语义轨；memorykernel 层不引 rag 保持 L1 纯净）。
type singleTextEmbedder struct{ inner *rag.OpenAIEmbedder }

func (s singleTextEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vecs, err := s.inner.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("bootstrap: embedding 空返回")
	}
	return vecs[0], nil
}
