// service_reposettings.go —— 服务侧按仓配置 API（v3.187.0）：repo_settings 表的
// 管理/读取面。写入走 admin 档（requiredScope 收口：PUT/DELETE /repo-settings/*），
// 值经 reposettings.Parse 校验后落规范 JSON；读面返回「按仓覆盖 + 全局生效值」
// 合并视图——部署方/外部 AI 一次看清某仓现在到底按什么参数在跑。

package service

import (
	"context"
	"errors"

	"github.com/oagentkit/obs/audit"
)

// 审计动作（service.go 常量同源）。
const (
	auditRepoSettingsUpdated audit.Action = "repo.settings_updated"
	auditRepoSettingsDeleted audit.Action = "repo.settings_deleted"
)

// validRepoSettingsPlatform platform 必须是部署装配过的平台（cfg.Platforms 是
// 权威词表——没装配的平台不可能有任务，配了也没消费面）。
func (s *Service) validRepoSettingsPlatform(platform string) bool {
	return validRepoSettingsPlatform(s, platform)
}

// validateRepoSettingsIdentity 仓库三元组校验（owner/repo 词法与 suppress 同口径）。
func validateRepoSettingsIdentity(platform, owner, repo string) error {
	if owner == "" || repo == "" {
		return errors.New("owner/repo 不能为空")
	}
	if len(owner) > 128 || len(repo) > 128 {
		return errors.New("owner/repo 过长（上限 128）")
	}
	if !isPlatformPath(owner) || !isPlatformPath(repo) {
		return errors.New("owner/repo 含非法字符")
	}
	return nil
}

// RepoSettingsView 单仓配置视图：found（是否有按仓覆盖）+ settings（覆盖集）
// + effective（生效值，含全局默认派生）。无覆盖时 effective 即全局默认——观测面
// 一次看清该仓当前真实参数。v6.7.0 P0-02：仓库维度判定收口（HTTP/MCP 共本出口
// ——此前 scope 档位有、仓库维度无，约束 token 可读任意仓配置）。
func (s *Service) RepoSettingsView(ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	return repoSettingsView(s, ctx, platform, owner, repo)
}

// UpsertRepoSettings 写入单仓覆盖。body 为 JSON（reposettings.Parse 校验）；
// 空覆盖拒收（语义等同 DELETE，用 DELETE 而不是写空对象）。
func (s *Service) UpsertRepoSettings(ctx context.Context, platform, owner, repo string, body []byte) (map[string]any, int) {
	return upsertRepoSettings(s, ctx, platform, owner, repo, body)
}

// DeleteRepoSettings 移除单仓覆盖（幂等：无配置也 200，附 removed 标记）。
// v6.7.0 P0-02：admin 档之上的仓库维度（同 Upsert）。
func (s *Service) DeleteRepoSettings(ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	return deleteRepoSettings(s, ctx, platform, owner, repo)
}

// ListRepoSettings 全部按仓配置（行数=仓库数量级，直接全量；观测面）。
// v6.7.0 P0-02：token 带仓库约束时约束外仓库的行不透出。
func (s *Service) ListRepoSettings(ctx context.Context) (map[string]any, int) {
	return listRepoSettings(s, ctx)
}

// repoMaxDiffCharsFor describe/testgen 等服务侧 diff 消费点的按仓生效值
// （repo_settings 覆盖 → 全局派生兜底）。读失败降级默认（与 loadRepoConfig
// 同纪律：配置面故障不阻断业务）。
func (s *Service) repoMaxDiffCharsFor(ctx context.Context, platform, owner, repo string) int {
	return repoMaxDiffCharsFor(s, ctx, platform, owner, repo)
}
