// service_repo_branches.go —— GET /repos/branches 仓库分支清单（v8.8.0 控制台
// 分支下拉数据面）：平台 SDK BranchManager + 默认分支（GetRepo 面，best-effort）。
package service

import (
	"context"
	"net/http"

	"github.com/yi-nology/argus/internal/argusctx"
)

// RepoBranches 仓库分支名清单 + 默认分支（read 档；方法内仓库维度判定）。
func (s *Service) RepoBranches(ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		return map[string]any{"error": "platform 未在部署装配（ARGUS_PLATFORM_*），无分支面"}, http.StatusBadRequest
	}
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/branches")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	if s.SDK == nil {
		return map[string]any{"error": "SDK 未装配，无分支面"}, http.StatusBadRequest
	}
	branches, err := s.SDK.ListBranches(ctx, platform, owner, repo)
	if err != nil {
		s.log().Error("argus.repo.branches_failed", "repo", owner+"/"+repo, "error", err.Error())
		return map[string]any{"error": "分支列举失败：" + err.Error()}, http.StatusBadGateway
	}
	// 默认分支 best-effort（部分平台 GetRepo 受限不影响清单主用途）。
	def := ""
	if d, derr := s.SDK.ResolveBranch(ctx, platform, owner, repo, ""); derr == nil {
		def = d
	}
	return map[string]any{"default_branch": def, "branches": branches}, http.StatusOK
}

// 编译期约束：SDKCtx 分支面签名（防止上游改名静默破坏出口）。
var _ = func(sdk *argusctx.SDKCtx) { _ = sdk.ListBranches }
