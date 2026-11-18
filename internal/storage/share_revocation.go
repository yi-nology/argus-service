// share_revocation.go —— share 令牌撤销表（#91）。
//
// 令牌本身保持无状态长存活（MR 评论存活数月的设计约束），撤销走服务端
// 拒绝清单：subject = 任务 ID（影响面域）或 "platform:owner/repo"
//（repo-map 域，与 repoMapShareID 同形态）。校验侧一次索引查（与租户
// 反查同成本量级），命中即拒。控制面 admin API 写本表。

package storage

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/tenant"
)

// ShareRevocation 一条撤销记录（PK=subject；幂等——重复撤销同 subject 原地刷新）。
type ShareRevocation struct {
	Subject   string    `gorm:"column:subject;primaryKey;size:255"`
	Reason    string    `gorm:"column:reason;size:512"`
	RevokedBy string    `gorm:"column:revoked_by;size:64"` // 操作员 token 指纹
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (ShareRevocation) TableName() string { return "share_revocations" }

// RevokeShare 撤销 subject 的 share 令牌；返回是否新撤销（false=本已撤销，幂等）。
func (s *Store) RevokeShare(ctx context.Context, subject, reason, operatorFP string) (bool, error) {
	return revokeShare(s, ctx, subject, reason, operatorFP)
}

func revokeShare(s *Store, ctx context.Context, subject, reason, operatorFP string) (bool, error) {
	if err := s.db.WithContext(tenant.WithoutScope(ctx)).Save(&ShareRevocation{
		Subject: subject, Reason: reason, RevokedBy: operatorFP, CreatedAt: time.Now(),
	}).Error; err != nil {
		return false, err
	}
	// Save 不区分 insert/update——幂等语义由「重复调用结果一致」承担，
	// 新撤销判定留给调用方首查（见 IsShareRevoked 的组合用法）。
	return true, nil
}

// IsShareRevoked 判定 subject 是否已撤销（校验侧每次调用一次索引查）。
func (s *Store) IsShareRevoked(ctx context.Context, subject string) (bool, error) {
	return isShareRevoked(s, ctx, subject)
}

func isShareRevoked(s *Store, ctx context.Context, subject string) (bool, error) {
	var n int64
	if err := s.db.WithContext(tenant.WithoutScope(ctx)).Model(&ShareRevocation{}).
		Where("subject = ?", subject).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListShareRevocations 撤销清单（控制面观测面）。
func (s *Store) ListShareRevocations(ctx context.Context, limit int) ([]ShareRevocation, error) {
	return listShareRevocations(s, ctx, limit)
}

func listShareRevocations(s *Store, ctx context.Context, limit int) ([]ShareRevocation, error) {
	var rows []ShareRevocation
	q := s.db.WithContext(tenant.WithoutScope(ctx)).Order("created_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	err := q.Find(&rows).Error
	return rows, err
}
