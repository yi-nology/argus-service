// service_impl_core.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/oagentkit/guardx/fence"
	"github.com/oagentkit/rag"
	"github.com/oekit/concurrency/async"
	"github.com/oekit/concurrency/syncx"
	"github.com/oekit/logx"
	argus "github.com/yi-nology/argus/biz/model/argus"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/tenant"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/writing"
	"github.com/yi-nology/go-git-platform/model"
)

// log 的包级实现（转发壳见原文件）。
func log(s *Service) Logger {
	if s.Log != nil {
		return s.Log
	}
	return logx.Nop()
}

// initCommentGate 的包级实现（转发壳见原文件）。
func initCommentGate(s *Service) {
	s.commentOnce.Do(func() {
		n := s.Cfg.CommentConcurrency
		if n <= 0 {
			n = 2
		}
		s.commentGate = syncx.NewGate(n)
	})
}

// submitComment 的包级实现（转发壳见原文件）。
func submitComment(s *Service, requestCtx context.Context, ev *model.NormalizedEvent, delivery string) bool {
	s.initCommentGate()
	release, ok := s.commentGate.TryAcquire()
	if !ok {
		obs.IncrCommentBusy()
		return false
	}
	timeout := s.Cfg.CommentTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(requestCtx), timeout)
	s.commentWG.Add(1)
	async.GoSafe(func() {
		defer cancel()
		defer s.commentWG.Done()
		defer release()
		s.HandleComment(ctx, ev, delivery)
	})
	return true
}

// drainComments 的包级实现（转发壳见原文件）。
func drainComments(s *Service, timeout time.Duration) {
	s.initCommentGate()
	done := make(chan struct{})
	async.GoSafe(func() {
		s.commentWG.Wait()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(timeout):
		s.log().Warn("argus.comment.drain_timeout", "timeout", timeout.String())
	}
}

// requirePlatform 的包级实现（转发壳见原文件）。
func requirePlatform(s *Service, platform string) (string, *config.PlatformConfig, bool) {
	platform = strings.ToLower(platform)
	pc, ok := s.Cfg.Platforms[platform]
	if !ok {
		return platform, nil, false
	}
	// GitHub App installation token 也算已配置
	if pc.Token == "" && !pc.GitHubAppEnabled() {
		return platform, nil, false
	}
	return platform, &pc, true
}

// handleInstallationEvent 的包级实现（转发壳见原文件）。
func handleInstallationEvent(s *Service, ev *model.NormalizedEvent, delivery string) *argus.WebhookResponse {
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID      int64 `json:"id"`
			Account struct {
				Login string `json:"login"`
				Type  string `json:"type"`
			} `json:"account"`
			RepositorySelection string `json:"repository_selection"`
		} `json:"installation"`
		Repositories []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal(ev.RawPayload, &payload); err != nil {
		s.log().Warn("argus.installation.parse_failed", "error", err.Error())
		return &argus.WebhookResponse{Status: "skipped", Reason: strPtr("installation 事件解析失败"), EventId: strPtr(delivery)}
	}

	repoNames := make([]string, 0, len(payload.Repositories))
	for _, r := range payload.Repositories {
		repoNames = append(repoNames, r.FullName)
	}

	s.log().Info("argus.installation.event",
		"action", payload.Action,
		"installation_id", payload.Installation.ID,
		"account", payload.Installation.Account.Login,
		"account_type", payload.Installation.Account.Type,
		"repository_selection", payload.Installation.RepositorySelection,
		"repos", repoNames,
		"event_id", delivery,
	)
	s.Audit.Log("github_app_installation",
		"action", payload.Action,
		"installation_id", payload.Installation.ID,
		"account", payload.Installation.Account.Login,
		"repos", repoNames,
	)
	return &argus.WebhookResponse{
		Status:  "accepted",
		Reason:  strPtr("installation event logged"),
		EventId: strPtr(delivery),
	}
}

// recordFalsePositiveKnowledge 的包级实现（转发壳见原文件）。
func recordFalsePositiveKnowledge(s *Service, ctx context.Context, platform, owner, repo, number, fp, file, comment string) {
	ms, ok := s.Knowledge.(*rag.MilvusStore)
	if !ok {
		return
	}
	var b strings.Builder
	b.WriteString("## 历史误报记录（开发者反馈）\n\n")
	b.WriteString(fmt.Sprintf("- 仓库：%s/%s#%s\n- 文件：%s\n- 指纹：%s\n\n", owner, repo, number, file, fp))
	b.WriteString("原审查意见：\n\n> " + fence.EscapeUntrusted(comment) + "\n\n")
	b.WriteString("开发者判定：以上意见为**误报**，同型问题在本仓库语境下不成立；检索到本条时请下调该类问题的置信度并核对历史语境。")
	// 租户键前缀（#84）：误报判例的语义检索键跨租户分桶（default 保持 legacy
	// 形态，存量条目原位兼容）。
	feedbackKey := fmt.Sprintf("feedback/%s/%s#%s/%s", owner, repo, number, fp[:8])
	if tid := tenant.FromContext(ctx); tid != "default" {
		feedbackKey = "t-" + tid + "/" + feedbackKey
	}
	if err := ms.Index(ctx, feedbackKey, b.String()); err != nil {
		s.log().Warn("argus.feedback.knowledge_failed", "fp", fp[:8], "error", err.Error())
		return
	}
	s.log().Info("argus.feedback.knowledge_recorded", "fp", fp[:8], "repo", owner+"/"+repo)
	s.Audit.Log("feedback.suppressed", "repo", owner+"/"+repo, "fp", fp[:8])
}

// reviewDiff 的包级实现（转发壳见原文件）。
func reviewDiff(s *Service, ctx context.Context, diff string) (string, error) {
	return (&writing.Service{LLM: s.LLM}).ReviewDiff(ctx, diff)
}

// statsOverview 的包级实现（转发壳见原文件）。
func statsOverview(s *Service, ctx context.Context, platform, owner, repo string, days int) (map[string]any, int) {
	if days <= 0 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	rows, err := s.Store.StatsOverview(ctx, time.Now().AddDate(0, 0, -days), platform, owner, repo)
	if err != nil {
		s.log().Error("argus.argus.stats_failed", "error", err.Error())
		return nil, http.StatusInternalServerError
	}
	filter := s.AuthorizeRowFilter(ctx)
	authzDeniedMetric(ctx, "/stats/overview")
	visible := make([]storage.RepoStats, 0, len(rows))
	var totals struct {
		repos, tasks, approved, commented, rejected, tokens, suppressed int64
	}
	for _, r := range rows {
		if filter != nil && !filter(r.Platform, r.Owner, r.Repo) {
			continue
		}
		visible = append(visible, r)
		totals.repos++
		totals.tasks += r.TasksDone
		totals.approved += r.Approved
		totals.commented += r.Commented
		totals.rejected += r.Rejected
		totals.tokens += r.Tokens
		totals.suppressed += r.Suppressed
	}
	return map[string]any{
		"window_days":  days,
		"generated_at": time.Now().Format(time.RFC3339),
		"repos":        visible,
		"repo_count":   totals.repos,
		"tasks_done":   totals.tasks,
		"verdicts": map[string]int64{
			"approve":         totals.approved,
			"comment":         totals.commented,
			"request_changes": totals.rejected,
		},
		"tokens_used":         totals.tokens,
		"findings_suppressed": totals.suppressed,
	}, http.StatusOK
}
