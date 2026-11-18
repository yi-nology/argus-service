// reposetting.go —— 服务侧按仓配置存储（v3.187.0）：repo_settings 表，键
// platform/owner/repo，值 JSON（internal/reposettings.Settings 规范形态）。
// 行数=仓库数（量级小），List 全量返回即可；写走「先查后写」事务式 upsert
// （与 prints 同纪律，SQLite/MySQL 双方言稳）。

package storage

import (
	"context"
	"time"
)

// RepoSetting 按仓配置行（tenant 入主键，#87：同坐标仓跨租户配置互不覆盖）。
type RepoSetting struct {
	TenantID string `gorm:"column:tenant_id;size:64;not null;default:default;primaryKey"`
	Platform string `gorm:"column:platform;size:16;primaryKey"`
	Owner    string `gorm:"column:owner;size:128;primaryKey"`
	Repo     string `gorm:"column:repo;size:128;primaryKey"`
	// SettingsJSON reposettings.Settings 规范 JSON（读取侧 Parse 单源校验）。
	SettingsJSON string `gorm:"column:settings_json;type:text;not null"`
	// UpdatedBy 写入者 token 指纹（TokenFPFromContext，sha256 前 8 hex，取证面）。
	UpdatedBy string    `gorm:"column:updated_by;size:64"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (RepoSetting) TableName() string { return "repo_settings" }

// GetRepoSetting 单仓配置；无配置返回 (nil, nil)（默认态是常态，不算错误）。
func (s *Store) GetRepoSetting(ctx context.Context, platform, owner, repo string) (*RepoSetting, error) {
	return getRepoSetting(s, ctx, platform, owner, repo)
}

// UpsertRepoSetting 写入（覆盖整行）；同一主键行更新而非新增。
func (s *Store) UpsertRepoSetting(ctx context.Context, row *RepoSetting) error {
	return upsertRepoSetting(s, ctx, row)
}

// DeleteRepoSetting 删除单仓配置；返回是否确有删除（幂等：无配置也成功）。
func (s *Store) DeleteRepoSetting(ctx context.Context, platform, owner, repo string) (bool, error) {
	return deleteRepoSetting(s, ctx, platform, owner, repo)
}

// ListRepoSettings 全量按仓配置（platform/owner/repo 字典序，观测面确定性）。
func (s *Store) ListRepoSettings(ctx context.Context) ([]*RepoSetting, error) {
	return listRepoSettings(s, ctx)
}
