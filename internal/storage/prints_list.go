package storage

// 抑制清单与修复状态聚合查询（Phase 4 候选清单/修复率数据面）。

import (
	"context"
)

// ListSuppressedPrints 全量 suppressed 指纹（误报判例候选清单扫描面；
// 平台无关聚合——按时间倒序，limit<=0 全量）。
func (s *Store) ListSuppressedPrints(ctx context.Context, limit int) ([]FindingPrint, error) {
	return listSuppressedPrints(s, ctx, limit)
}

// FixStatusCount 修复状态计数行（修复率聚合面）。
type FixStatusCount struct {
	FixStatus string
	N         int
}

// CountFixStatuses 按 fix_status 分组计数（fix_status != ” 的修复链任务）。
func (s *Store) CountFixStatuses(ctx context.Context) ([]FixStatusCount, error) {
	return countFixStatuses(s, ctx)
}
