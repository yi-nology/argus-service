// merger.go —— Merger 合并器主面（design §3.3，全部确定性）：类型定义、
// 构造面与 Merge 编排；轮内归并/入库见 merger_round.go，标注面见
// merger_annotate.go，学习环闸门见 merger_learning.go。

package dispatch

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
	"github.com/yi-nology/argus/internal/plugin"
)

// plugin.MergedFindings Merger 产出。
// Merger 合并器（design §3.3，全部确定性）：
//  1. severity 入口归一化（越界归 low + 告警）；
//  2. 轮内精确指纹去重（规范化评论文本哈希 + file；冲突 severity 取高）；
//  3. 跨轮次指纹入库/更新，suppressed 的不进报告；
//  4. 插件失败/跳过 → 降级注明，不拖垮整体；
//  5. 学习环开启时：低置信模式标注 + 仅 low 条目按 HardFloor 丢弃。
type Merger struct {
	store    domain.PrintStore
	patterns domain.PatternStore
	learn    learning.Config
	log      logx.Logger
}

func NewMerger(store domain.PrintStore, log logx.Logger) *Merger {
	return &Merger{store: store, log: log, learn: learning.DefaultConfig()}
}

// WithLearning 接入模式级后悔学习环（nil store 或 Enabled=false 时旁路）。
func (m *Merger) WithLearning(ps domain.PatternStore, cfg learning.Config) *Merger {
	m.patterns = ps
	m.learn = cfg
	return m
}

// Merge 执行合并。pluginNameOf 提供 finding 归属（ Outcome 遍历时注入）。
func (m *Merger) Merge(ctx context.Context, pr plugin.PRContext, taskID string, outcomes []Outcome, at time.Time) (*plugin.MergedFindings, error) {
	defer ReportLayerExec(ctx, plugin.LayerMerger)
	out := &plugin.MergedFindings{}
	order := m.mergeInRound(ctx, outcomes, out)
	pendings := m.persistPrints(ctx, pr, taskID, at, order)
	m.applyLearning(ctx, pr, at, out, pendings)

	slices.SortStableFunc(out.Findings, func(a, b domain.Finding) int {
		if c := cmp.Compare(plugin.SeverityRank(a.Severity), plugin.SeverityRank(b.Severity)); c != 0 {
			return -c
		}
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return cmp.Compare(a.Line, b.Line)
	})
	return out, nil
}

func abs(n int) int { return max(n, -n) }
