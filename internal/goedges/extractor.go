// Package goedges —— semsearch.EdgeAugmenter 的 gotyped 适配器（收编：抽取
// 判据本体已下沉上游 textx gotyped 嵌套模块 v0.13.0，oagentkit/textx#106——
// go/packages+typeutil.Callee 精确边、接口分派丢弃、失败闩语义、文件上限
// 全在上游，含 testdata 拓扑全量断言）。
//
// 本包只剩接缝：gotyped.Symbol{Dir,Recv,Name} → semsearch.NodeKey 契约键
// （两段格式逐字一致：dir.Recv.Name / dir.Name，根目录无前缀——与上游
// Symbol.String() 同构，映射经 NodeKey 单源构造不各自拼串）+ MaxFiles 透传。
// ARGUS_GRAPH_PRECISE opt-in 时经 bootstrap 以 semsearch.WithEdges 注入；
// 行为测试（fixture 全形态）在本包 extractor_test.go 保持不动，过适配器公
// 共 Edges 面全量回归。
package goedges

import (
	"context"

	"github.com/oagentkit/textx/gotyped"

	"github.com/yi-nology/argus/internal/semsearch"
)

// DefaultMaxFiles Go 源文件总数上限（透传上游缺省；semsearch 图面容量封顶
// 同款哲学：宁可降级词法边不可失控）。
const DefaultMaxFiles = gotyped.DefaultMaxFiles

// Extractor 精确调用边抽取器（semsearch.EdgeAugmenter 实现）。
type Extractor struct {
	// MaxFiles Go 源文件总数上限（零/负值按 DefaultMaxFiles）。
	MaxFiles int
}

// New 构建缺省上限的抽取器（bootstrap 注入用）。
func New() *Extractor {
	return &Extractor{MaxFiles: DefaultMaxFiles}
}

// 接口契约钉死：本包唯一存在理由是实现 semsearch.EdgeAugmenter。
var _ semsearch.EdgeAugmenter = (*Extractor)(nil)

// Edges 返回全仓精确调用边（EdgeAugmenter 实现）：gotyped 结构化边 →
// NodeKey 契约键。失败语义（无 go.mod/包带错误/超上限）原样透传上游
// error——调用方置失败闩降级词法边。
func (x *Extractor) Edges(ctx context.Context, dir string) ([][2]string, error) {
	ge, err := (&gotyped.Extractor{MaxFiles: x.MaxFiles}).Edges(ctx, dir)
	if err != nil {
		return nil, err
	}
	out := make([][2]string, 0, len(ge))
	for _, e := range ge {
		out = append(out, [2]string{nodeKey(e.From), nodeKey(e.To)})
	}
	return out, nil
}

// nodeKey gotyped 端点 → semsearch.NodeKey 契约键（单源构造）。
func nodeKey(s gotyped.Symbol) string {
	return semsearch.NodeKey(s.Dir, s.Recv, s.Name)
}
