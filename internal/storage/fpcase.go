package storage

// 组织级误报判例桶表（批 E spec §3.1）：只存模式与审计锚，不复制意见全文。

import (
	"context"
	"time"
)

// FPCaseBucket 判例桶行。
type FPCaseBucket struct {
	ID          uint      `gorm:"primaryKey"`
	Pattern     string    `gorm:"column:pattern;size:64;uniqueIndex"`
	Kind        string    `gorm:"column:kind;size:64"`         // 归一类目（file 形态投影）
	Org         string    `gorm:"column:org;size:128;index"`   // 聚合轴（Owner，不跨 org）
	SampleRepo  string    `gorm:"column:sample_repo;size:256"` // 首次来源仓（审计锚）
	SamplePrint string    `gorm:"column:sample_print;size:80"`
	Comment     string    `gorm:"column:comment;type:text"` // 样例评语（骨架输入，单份）
	HitCount    int       `gorm:"column:hit_count;default:0"`
	Status      string    `gorm:"column:status;size:16;default:candidate;index"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (FPCaseBucket) TableName() string { return "fp_case_buckets" }

// UpsertFPCase 写侧聚合（批 E §3.3）：Pattern 唯一键——新模式落 candidate
// + 来源锚；既有模式 HitCount++（抑制信号再确认）。读-改-写在 withWrite
// 事务内（进程内互斥；不用 ON CONFLICT 表达式=避开 mysql VALUES() 与
// pg/sqlite excluded() 的方言差；多副本竞争最多少加一次计数，无害）。
func (s *Store) UpsertFPCase(ctx context.Context, bucket FPCaseBucket) error {
	return upsertFPCase(s, ctx, bucket)
}

// ListFPCaseCandidates 待审清单（candidate 态，按 HitCount 倒序）。
func (s *Store) ListFPCaseCandidates(ctx context.Context, limit int) ([]FPCaseBucket, error) {
	return listFPCaseCandidates(s, ctx, limit)
}

// ConfirmFPCase 人工确认（admin 端点消费）：candidate → confirmed/rejected。
func (s *Store) ConfirmFPCase(ctx context.Context, pattern, status string) error {
	return confirmFPCase(s, ctx, pattern, status)
}

// MatchFPCase 审查面命中查询（confirmed 才消费——candidate 不提示）：
// 按 Pattern 精确查已确认判例。
func (s *Store) MatchFPCase(ctx context.Context, pattern string) (*FPCaseBucket, error) {
	return matchFPCase(s, ctx, pattern)
}
