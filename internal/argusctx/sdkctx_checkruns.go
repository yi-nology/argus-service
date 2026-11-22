// sdkctx_checkruns.go —— GitHub Check Runs 双面判定（T16 合并门补面）。
//
// provider.ListCommitStatuses 只覆盖 Commit Statuses API；GitHub Actions 的
// checks 走 Check Runs API，此前 Actions-only 仓在合并门被误判 unknown 阻断。
// 本文件用裸 HTTP 补第二面（provider 未暴露 CheckRunsManager 时的诚实补位）。
// 判定：两面取最坏——任一 failure → failure；任一 pending/queued/in_progress →
// pending；两面全 success（或一面空）→ success；两面皆空/皆失败读取 → unknown。
package argusctx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// checkRunsAPI GitHub Check Runs（REST）。
const checkRunsAPI = "https://api.github.com/repos/%s/%s/commits/%s/check-runs?per_page=100"

type checkRunsPage struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Name       string `json:"name"`
	} `json:"check_runs"`
}

// evalGitHubCheckRuns 读 Check Runs 第二面（success/pending/failure/unknown）。
func (s *SDKCtx) evalGitHubCheckRuns(ctx context.Context, owner, repo, sha string) (state, detail string) {
	if s == nil || s.rawTokenFor == nil {
		return CIStateUnknown, "no token source"
	}
	tok := s.rawTokenFor("github")
	if tok == "" {
		return CIStateUnknown, "no github token"
	}
	url := fmt.Sprintf(checkRunsAPI, owner, repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return CIStateUnknown, "check-runs request build failed"
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req = req.WithContext(cctx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return CIStateUnknown, "check-runs read failed"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CIStateUnknown, fmt.Sprintf("check-runs http %d", resp.StatusCode)
	}
	var page checkRunsPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return CIStateUnknown, "check-runs decode failed"
	}
	if len(page.CheckRuns) == 0 {
		return CIStateUnknown, "no check runs on head"
	}

	hasPending, hasFailure, hasSuccess := false, false, false
	for _, cr := range page.CheckRuns {
		switch strings.ToLower(cr.Status) {
		case "completed":
			switch strings.ToLower(cr.Conclusion) {
			case "success", "neutral", "skipped":
				hasSuccess = true
			default: // failure, timed_out, action_required, cancelled, stale
				hasFailure = true
			}
		default: // queued, in_progress, requested, waiting
			hasPending = true
		}
	}
	switch {
	case hasFailure:
		return CIStateFailure, "check runs have failure"
	case hasPending:
		return CIStatePending, "check runs pending"
	case hasSuccess:
		return CIStateSuccess, "all check runs success"
	default:
		return CIStateUnknown, "check runs unclassified"
	}
}

// combineCI 两面（statuses + check-runs）取最坏。
func combineCI(a, b string) string {
	rank := func(s string) int {
		switch s {
		case CIStateFailure:
			return 4
		case CIStatePending:
			return 3
		case CIStateUnknown:
			return 2
		case CIStateSuccess:
			return 1
		default:
			return 2
		}
	}
	if rank(a) >= rank(b) {
		return a
	}
	return b
}
