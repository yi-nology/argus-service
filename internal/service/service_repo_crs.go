// service_repo_crs.go —— GET /repos/crs 仓库 PR/MR 列表（v8.8.1 控制台
// PR 号下拉数据面）：平台 ChangeRequest 面，等价 poller 的在开清单口径。
package service

import (
	"context"
	"net/http"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
)

const (
	defaultRepoCRsLimit = 30
	maxRepoCRsLimit     = 100
)

// RepoCRs 仓库 PR/MR 列表（read 档；默认 opened，方法内仓库维度判定）。
func (s *Service) RepoCRs(ctx context.Context, platform, owner, repo, state string, limit int) (map[string]any, int) {
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		return map[string]any{"error": "platform 未在部署装配（ARGUS_PLATFORM_*），无 PR 面"}, http.StatusBadRequest
	}
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repos/crs")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	if s.SDK == nil {
		return map[string]any{"error": "SDK 未装配，无 PR 面"}, http.StatusBadRequest
	}
	// 状态词表（控制台只需要 opened；其余保留运维排查面）。
	switch state {
	case "":
		state = string(model.CRStateOpened)
	case string(model.CRStateOpened), string(model.CRStateClosed), string(model.CRStateMerged):
	default:
		return map[string]any{"error": "state 只支持 opened|closed|merged（空=opened）"}, http.StatusBadRequest
	}
	if limit <= 0 {
		limit = defaultRepoCRsLimit
	}
	if limit > maxRepoCRsLimit {
		limit = maxRepoCRsLimit
	}
	crs, _, err := s.SDK.ListCRs(ctx, platform, provider.ListCROptions{
		Owner: owner, Repo: repo, State: model.CRState(state), PerPage: limit,
	})
	if err != nil {
		s.log().Error("argus.repo.crs_failed", "repo", owner+"/"+repo, "error", err.Error())
		return map[string]any{"error": "PR 列表拉取失败：" + err.Error()}, http.StatusBadGateway
	}
	items := make([]map[string]any, 0, len(crs))
	for _, cr := range crs {
		if cr == nil || cr.Number == "" {
			continue
		}
		author := ""
		if cr.Author != nil {
			author = cr.Author.Username
		}
		items = append(items, map[string]any{
			"number":        cr.Number,
			"title":         cr.Title,
			"state":         string(cr.State),
			"draft":         cr.Draft,
			"source_branch": cr.SourceBranch,
			"target_branch": cr.TargetBranch,
			"author":        author,
			"head_sha":      cr.HeadSHA,
			"web_url":       cr.WebURL,
		})
		if len(items) >= limit {
			break
		}
	}
	return map[string]any{"items": items}, http.StatusOK
}
