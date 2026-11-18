// knowledgefilter 包 —— rag.KnowledgeService 的租户过滤装饰器（#84/#92）。
//
// 上游 rag 的 Retrieve filter 白名单只有 file/heading 精确匹配，无法表达
// 「本租户键空间」前缀语义——装饰器用超取+宿主侧过滤补齐：Retrieve 按
// overFetch 倍取候选，按 chunk 的 file 元数据归属租户裁剪后截断 topK。
// 归属约定（与 dream/feedback 写侧 dreamKnowledgeKey 同源）：
//   - "t-<tenant>/" 前缀键 → 该租户；
//   - 无前缀键（legacy，含 KnowledgeDir 本地语料）→ default 租户。
// 装配单点：bootstrap.buildKnowledge 出口包一层——service.search_knowledge、
// R3 AsTool 工具、R4 预检索全部消费过滤后检索（无一处漏网是机制目标）。
package knowledgefilter

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/components/tool"

	"github.com/oagentkit/rag"

	"github.com/yi-nology/argus/internal/tenant"
)

// overFetch 超取倍数：过滤后截断 topK，候选池放大防「他租户高分挤占」致
// 本租户命中不足（4x 经验值；不足再调）。
const overFetch = 4

// Wrap 包一层租户过滤（nil/Noop 原样透传，调用方零感知）。
func Wrap(inner rag.KnowledgeService) rag.KnowledgeService {
	if inner == nil {
		return nil
	}
	if _, ok := inner.(*rag.Noop); ok {
		return inner
	}
	return &tenantKnowledge{inner: inner}
}

type tenantKnowledge struct {
	inner rag.KnowledgeService
}

// Retrieve 超取 + 宿主侧租户过滤 + 截断。
func (t *tenantKnowledge) Retrieve(ctx context.Context, query string, topK int, filter rag.Filter) ([]rag.Chunk, error) {
	if topK <= 0 {
		topK = 5
	}
	chunks, err := t.inner.Retrieve(ctx, query, topK*overFetch, filter)
	if err != nil {
		return nil, err
	}
	tid := tenant.FromContext(ctx)
	// 新分配（不原地复用 inner 返回切片——上游可能持有/复用同一底层数组）。
	out := make([]rag.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if chunkOwnedBy(c, tid) {
			out = append(out, c)
		}
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

// AsTool 以装饰器自身构建检索工具——工具调用 ctx 流入 Retrieve，
// 过滤链无一处漏网（#92：search_knowledge 此前传 nil filter 零过滤）。
func (t *tenantKnowledge) AsTool() tool.BaseTool {
	kt, err := rag.NewSearchTool(t)
	if err != nil {
		return nil // 构造失败等同知识工具不可用（workflow 对 nil 工具跳过）
	}
	return kt
}

// chunkOwnedBy 键归属判定："t-<tid>/" 前缀键 → 仅该租户可见（租户私有语料：
// dream 导出/误报回流）；无前缀 legacy 键（KnowledgeDir 组织规范等共享基线
// 语料）→ 全部租户可见——组织规范本来就是跨租户共享的设计语义。
func chunkOwnedBy(c rag.Chunk, tid string) bool {
	f := c.Metadata["file"]
	if strings.HasPrefix(f, "t-") {
		if i := strings.IndexByte(f, '/'); i > 2 {
			return f[2:i] == tid
		}
	}
	return true
}
