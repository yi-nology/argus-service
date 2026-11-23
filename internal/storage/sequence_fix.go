// sequence_fix.go —— PG 自增序列对齐（收编：实现已下沉上游 gormx v0.15.0
// RepairSequences，oekit/oekit#226——生产 P0 实锤的根治面：dbcopy 显式 ID
// 灌库后序列停在低位，新插入永久 23505 且只打挂低流量表；上游幂等、不
// 支持方言显式报错、活体 PG 集成测试复刻事故全形态）。
//
// 本文件只剩 Store 门面：方言门控（PG 以外静默跳过——bootstrap 启动期
// 自愈对全方言统一调用）+ 委托上游。dbcopy 侧直接调 gormx.RepairSequencesDB
// 等价入口（见 internal/cli/dbcopy/run.go）。

package storage

import (
	"context"

	"github.com/oekit/gormx"
)

// RepairSequences Store 门面：仅 PG 方言执行（SQLite/MySQL 语义不需要，
// 返回空）；bootstrap 启动期调用（修复即 WARN 留痕 + sequences_repaired
// 指标）。判据本体与事故复刻测试在上游 gormx。
func (s *Store) RepairSequences(ctx context.Context) ([]string, error) {
	if s.dialect != DialectPostgres {
		return nil, nil
	}
	return gormx.RepairSequences(ctx, s.db)
}
