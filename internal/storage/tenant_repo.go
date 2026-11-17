// tenant_repo.go —— tenant_repos 映射表（#82/#103）：仓库坐标 → 租户归属。
//
// webhook 入站没有 Bearer token，租户归属只能从「平台+坐标」反查。映射行
// 由控制面 admin API 维护（多租户开户=登记映射）；无映射的坐标回落 default
// 租户（存量单租户部署零变化）。查询侧走 tenant.WithoutScope（映射本身是
// 跨租户路由表，谓词注入会让反查永远落空）。

package storage

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/tenant"
)

// TenantRepo 一条仓库→租户映射（PK = 坐标；一仓一租户，登记即独占）。
type TenantRepo struct {
	TenantID  string    `gorm:"column:tenant_id;size:64;not null;index"`
	Platform  string    `gorm:"column:platform;size:16;primaryKey"`
	Owner    string    `gorm:"column:owner;size:128;primaryKey"`
	Repo      string    `gorm:"column:repo;size:128;primaryKey"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (TenantRepo) TableName() string { return "tenant_repos" }

// ResolveTenantRepo 反查坐标的租户归属；无映射返回 (tenant.Default, false)。
// 调用方据 ok 决定是否落审计（未映射坐标 = default 租户隐式归属）。
func (s *Store) ResolveTenantRepo(ctx context.Context, platform, owner, repo string) (string, bool) {
	return resolveTenantRepo(s, ctx, platform, owner, repo)
}

// resolveTenantRepo 的包级实现（转发壳见原文件）。
func resolveTenantRepo(s *Store, ctx context.Context, platform, owner, repo string) (string, bool) {
	var row TenantRepo
	err := s.db.WithContext(tenant.WithoutScope(ctx)).
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo).
		Take(&row).Error
	if err != nil {
		return tenant.Default, false
	}
	return tenant.Normalize(row.TenantID), true
}

// UpsertTenantRepo 登记/变更映射（控制面 admin 写面；同坐标换租户=覆盖）。
func (s *Store) UpsertTenantRepo(ctx context.Context, row TenantRepo) error {
	return upsertTenantRepo(s, ctx, row)
}

// upsertTenantRepo 的包级实现（转发壳见原文件）。
func upsertTenantRepo(s *Store, ctx context.Context, row TenantRepo) error {
	return s.db.WithContext(tenant.WithoutScope(ctx)).Save(&row).Error
}

// DeleteTenantRepo 删除映射（租户退订/下线）；返回是否确有删除。
func (s *Store) DeleteTenantRepo(ctx context.Context, platform, owner, repo string) (bool, error) {
	return deleteTenantRepo(s, ctx, platform, owner, repo)
}

// deleteTenantRepo 的包级实现（转发壳见原文件）。
func deleteTenantRepo(s *Store, ctx context.Context, platform, owner, repo string) (bool, error) {
	res := s.db.WithContext(tenant.WithoutScope(ctx)).
		Where("platform = ? AND owner = ? AND repo = ?", platform, owner, repo).
		Delete(&TenantRepo{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListTenantRepos 全量映射清单（控制面枚举面；行数=租户仓库数，量级小）。
func (s *Store) ListTenantRepos(ctx context.Context) ([]TenantRepo, error) {
	return listTenantRepos(s, ctx)
}

// listTenantRepos 的包级实现（转发壳见原文件）。
func listTenantRepos(s *Store, ctx context.Context) ([]TenantRepo, error) {
	var rows []TenantRepo
	err := s.db.WithContext(tenant.WithoutScope(ctx)).Order("platform, owner, repo").Find(&rows).Error
	return rows, err
}
