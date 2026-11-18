// service_impl_repo.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/yi-nology/argus/internal/reposettings"
	"github.com/yi-nology/argus/internal/storage"
)

// repoAllowedFor 的包级实现（转发壳见原文件）。
func repoAllowedFor(s *Service, ctx context.Context, platform, owner, repo string) bool {
	target := platform + ":" + owner + "/" + repo
	if s.Cfg != nil && len(s.Cfg.RepoAllowlist) > 0 {
		hit := false
		for _, e := range s.Cfg.RepoAllowlist {
			if matchRepoPattern(e, target) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if tp := TokenReposFromContext(ctx); tp != "" && !matchRepoPattern(tp, target) {
		return false
	}
	return true
}

// taskVisible 的包级实现（转发壳见原文件）。
func taskVisible(s *Service, ctx context.Context, platform, owner, repo string) bool {
	tp := TokenReposFromContext(ctx)
	if tp == "" || platform == "local" {
		return true
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}

// taskListable 的包级实现（转发壳见原文件）。
func taskListable(s *Service, ctx context.Context, platform, owner, repo string) bool {
	tp := TokenReposFromContext(ctx)
	if tp == "" {
		return true
	}
	if platform == "local" {
		return false
	}
	return matchRepoPattern(tp, platform+":"+owner+"/"+repo)
}

// validRepoSettingsPlatform 的包级实现（转发壳见原文件）。
func validRepoSettingsPlatform(s *Service, platform string) bool {
	if platform == "" {
		return false
	}
	_, ok := s.Cfg.Platforms[platform]
	return ok
}

// repoSettingsView 的包级实现（转发壳见原文件）。
func repoSettingsView(s *Service, ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repo-settings/:platform/:owner/:repo")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	effective := map[string]int{
		"monthly_token_budget": s.Cfg.RepoMonthlyTokenBudget,
		"task_token_budget":    s.Cfg.EffectiveTaskTokenBudget(),
		"max_diff_chars":       s.Cfg.MaxDiffCharsResolved(),
	}
	row, err := s.Store.GetRepoSetting(ctx, platform, owner, repo)
	if err != nil {
		s.log().Error("argus.api.repo_settings_get_failed", "error", err.Error())
		return map[string]any{"error": "查询失败"}, http.StatusInternalServerError
	}
	out := map[string]any{
		"platform": platform, "owner": owner, "repo": repo,
		"found": false, "settings": nil, "effective": effective,
		"source": "global_default",
	}
	if row != nil {
		rs, perr := reposettings.Parse([]byte(row.SettingsJSON))
		if perr != nil {
			// 落库值损坏不应炸读面：降级为默认态并如实标注。
			s.log().Warn("argus.api.repo_settings_corrupt", "error", perr.Error())
			out["corrupt"] = perr.Error()
			return out, http.StatusOK
		}
		if !rs.IsZero() {
			effective["monthly_token_budget"] = rs.MonthlyTokenBudgetOr(effective["monthly_token_budget"])
			effective["task_token_budget"] = rs.TaskTokenBudgetOr(effective["task_token_budget"])
			effective["max_diff_chars"] = rs.MaxDiffCharsOr(effective["max_diff_chars"])
			out["found"] = true
			out["source"] = "repo_override"
		}
		out["settings"] = rs
		out["updated_by"] = row.UpdatedBy
		out["updated_at"] = row.UpdatedAt
	}
	out["effective"] = effective
	return out, http.StatusOK
}

// upsertRepoSettings 的包级实现（转发壳见原文件）。
func upsertRepoSettings(s *Service, ctx context.Context, platform, owner, repo string, body []byte) (map[string]any, int) {
	if !s.validRepoSettingsPlatform(platform) {
		return map[string]any{"error": fmt.Sprintf("platform %q 未在部署装配（ARGUS_PLATFORM_*），无消费面", platform)}, http.StatusBadRequest
	}
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	// v6.7.0 P0-02：admin 档之上的仓库维度——约束 token 不可写约束外仓库。
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repo-settings/:platform/:owner/:repo")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	rs, err := reposettings.Parse(body)
	if err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if rs.IsZero() {
		return map[string]any{"error": "空覆盖无意义：不设置任何旋钮请用 DELETE 移除按仓配置"}, http.StatusBadRequest
	}
	norm, err := rs.Normalize()
	if err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	row := &storage.RepoSetting{
		Platform: platform, Owner: owner, Repo: repo,
		SettingsJSON: string(norm), UpdatedBy: TokenFPFromContext(ctx),
		UpdatedAt: time.Now(),
	}
	if err := s.Store.UpsertRepoSetting(ctx, row); err != nil {
		s.log().Error("argus.api.repo_settings_upsert_failed", "error", err.Error())
		return map[string]any{"error": "写入失败"}, http.StatusInternalServerError
	}
	s.audit(ctx, auditRepoSettingsUpdated, "platform", platform, "owner", owner,
		"repo", repo, "settings", string(norm), "token_fp", row.UpdatedBy)
	view, code := s.RepoSettingsView(ctx, platform, owner, repo)
	if code != http.StatusOK {
		// 写成功但视图失败：如实返回写入完成（视图是读面问题）。
		return map[string]any{"written": true, "settings_json": string(norm)}, http.StatusOK
	}
	return view, code
}

// deleteRepoSettings 的包级实现（转发壳见原文件）。
func deleteRepoSettings(s *Service, ctx context.Context, platform, owner, repo string) (map[string]any, int) {
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/repo-settings/:platform/:owner/:repo")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	removed, err := s.Store.DeleteRepoSetting(ctx, platform, owner, repo)
	if err != nil {
		s.log().Error("argus.api.repo_settings_delete_failed", "error", err.Error())
		return map[string]any{"error": "删除失败"}, http.StatusInternalServerError
	}
	if removed {
		s.audit(ctx, auditRepoSettingsDeleted, "platform", platform, "owner", owner,
			"repo", repo, "token_fp", TokenFPFromContext(ctx))
	}
	return map[string]any{"removed": removed, "source": "global_default"}, http.StatusOK
}

// listRepoSettings 的包级实现（转发壳见原文件）。
func listRepoSettings(s *Service, ctx context.Context) (map[string]any, int) {
	rows, err := s.Store.ListRepoSettings(ctx)
	if err != nil {
		s.log().Error("argus.api.repo_settings_list_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	filter := s.AuthorizeRowFilter(ctx)
	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		if filter != nil && !filter(r.Platform, r.Owner, r.Repo) {
			continue
		}
		rs, perr := reposettings.Parse([]byte(r.SettingsJSON))
		if perr != nil {
			items = append(items, map[string]any{
				"platform": r.Platform, "owner": r.Owner, "repo": r.Repo,
				"corrupt": perr.Error(),
			})
			continue
		}
		items = append(items, map[string]any{
			"platform": r.Platform, "owner": r.Owner, "repo": r.Repo,
			"settings": rs, "updated_by": r.UpdatedBy, "updated_at": r.UpdatedAt,
		})
	}
	return map[string]any{"items": items, "total": len(items)}, http.StatusOK
}

// repoMaxDiffCharsFor 的包级实现（转发壳见原文件）。
func repoMaxDiffCharsFor(s *Service, ctx context.Context, platform, owner, repo string) int {
	def := s.Cfg.MaxDiffCharsResolved()
	if s.Store == nil {
		return def
	}
	row, err := s.Store.GetRepoSetting(ctx, platform, owner, repo)
	if err != nil || row == nil {
		return def
	}
	rs, perr := reposettings.Parse([]byte(row.SettingsJSON))
	if perr != nil {
		return def
	}
	return rs.MaxDiffCharsOr(def)
}
