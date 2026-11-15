package argusctx

import (
	"context"
	"fmt"
	"time"

	datacache "github.com/oekit/data/cache"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/plugin"
)

// resolveBranch 实现 SDKCtx.ResolveBranch（默认分支解析 + 缓存）。
func resolveBranch(s *SDKCtx, ctx context.Context, platform, owner, repo, branch string) (string, error) {
	if branch != "" {
		return branch, nil
	}
	key := platform + "/" + owner + "/" + repo
	if v, err := s.branchCache().Get(ctx, key); err == nil && v != nil {
		return *v, nil
	}
	info, err := s.GetRepo(ctx, platform, owner, repo)
	if err != nil {
		return "", err
	}
	if info.DefaultBranch == "" {
		return "", fmt.Errorf("仓库 %s/%s/%s 默认分支为空", platform, owner, repo)
	}
	_ = s.branchCache().Set(ctx, key, &info.DefaultBranch)
	return info.DefaultBranch, nil
}

// loadRepoConfigAuto 实现 SDKCtx.LoadRepoConfigAuto（默认分支 + 仓库配置单步入口）。
func loadRepoConfigAuto(s *SDKCtx, ctx context.Context, platform, owner, repo, branch string) (*plugin.RepoConfig, error) {
	b, err := s.ResolveBranch(ctx, platform, owner, repo, branch)
	if err != nil {
		return nil, err
	}
	return s.LoadRepoConfig(ctx, platform, owner, repo, b)
}

// branchCacheOf 实现 SDKCtx.branchCache（默认分支缓存懒建）。
func branchCacheOf(s *SDKCtx) *datacache.LocalCache[string] {
	s.branchMu.Lock()
	defer s.branchMu.Unlock()
	if s.branchCch == nil {
		s.branchCch = datacache.NewLocalCache(datacache.LocalConfig[string]{
			TTL:     5 * time.Minute,
			MaxSize: 200,
		})
	}
	return s.branchCch
}

// loadRepoConfig 实现 SDKCtx.LoadRepoConfig（默认分支读 .reviewagents.yaml）。
func loadRepoConfig(s *SDKCtx, ctx context.Context, platform, owner, repo, defaultBranch string) (*plugin.RepoConfig, error) {
	const path = ".reviewagents.yaml"
	content, err := s.GetFileContent(ctx, platform, owner, repo, path, defaultBranch)
	if err != nil {
		if provider.IsNotFound(err) {
			return plugin.EmptyRepoConfig(), nil
		}
		return nil, err
	}
	rc, err := plugin.ParseRepoConfig([]byte(content))
	if err != nil {
		// 仓库配置是敌对输入：解析失败按无配置处理 + 告警，不炸任务
		s.log.Warn("argus.sdkctx.repo_config_invalid", "repo", owner+"/"+repo, "error", err.Error())
		return plugin.EmptyRepoConfig(), nil
	}
	return rc, nil
}
