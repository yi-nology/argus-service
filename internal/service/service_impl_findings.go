// service_impl_findings.go —— 意见治理台包级实现（转发壳见 service_findings.go）。
// 读面纪律：平台装配校验 → 仓库三元组词法 → 仓库维度授权 → 状态词表 →
// limit 钳制 → 列表 + 计数单源。注释与 repo_settings 读面同口径。

package service

import (
	"context"
	"net/http"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

const (
	defaultListFindingsLimit = 100
	maxListFindingsLimit     = 500
)

// unixOrZero 零值时间返回 0（gorm 未写入时间列时避免 1 年负时间戳）。
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// listFindings 的包级实现（转发壳见 service_findings.go）。
func listFindings(s *Service, ctx context.Context, platform, owner, repo, status string, limit int) (map[string]any, int) {
	platform, _, ok := s.requirePlatform(platform)
	if !ok {
		return map[string]any{"error": "platform 未在部署装配（ARGUS_PLATFORM_*），无消费面"}, http.StatusBadRequest
	}
	if err := validateRepoSettingsIdentity(platform, owner, repo); err != nil {
		return map[string]any{"error": err.Error()}, http.StatusBadRequest
	}
	switch status {
	case "", storage.PrintOpen, storage.PrintSuppressed:
	default:
		return map[string]any{"error": "status 只支持 open|suppressed（空=全部）"}, http.StatusBadRequest
	}
	if !s.Authorize(ctx, platform, owner, repo) {
		authzDeniedMetric(ctx, "/findings")
		return map[string]any{"error": ErrAuthzDenied.Error()}, http.StatusForbidden
	}
	if limit <= 0 {
		limit = defaultListFindingsLimit
	}
	if limit > maxListFindingsLimit {
		limit = maxListFindingsLimit
	}

	rows, err := s.Store.ListPrintsByRepo(ctx, platform, owner, repo, status, limit)
	if err != nil {
		s.log().Error("argus.api.list_findings_failed", "repo", owner+"/"+repo, "error", err.Error())
		return map[string]any{"error": "查询失败"}, http.StatusInternalServerError
	}
	summary, err := s.Store.PrintRepoSummary(ctx, platform, owner, repo)
	if err != nil {
		// 计数失败不阻断清单：返回空计数并标注，前端仍可操作。
		s.log().Warn("argus.api.list_findings_summary_failed", "repo", owner+"/"+repo, "error", err.Error())
		summary = storage.PrintRepoSummary{}
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		items = append(items, map[string]any{
			"platform":      r.Platform,
			"owner":         r.Owner,
			"repo":          r.Repo,
			"number":        r.Number,
			"fingerprint":   r.Print,
			"file":          r.File,
			"line":          r.Line,
			"severity":      r.Severity,
			"comment":       r.Comment,
			"status":        r.Status,
			"plugin":        r.Plugin,
			"times_seen":    r.TimesSeen,
			"regret_count":  r.RegretCount,
			"first_seen_at": unixOrZero(r.FirstSeenAt),
			"last_seen_at":  unixOrZero(r.LastSeenAt),
		})
	}
	return map[string]any{
		"platform": platform,
		"owner":    owner,
		"repo":     repo,
		"status":   status,
		"limit":    limit,
		"findings": items,
		"counts": map[string]int64{
			"total":      summary.Total,
			"open":       summary.Open,
			"suppressed": summary.Suppressed,
		},
	}, http.StatusOK
}
