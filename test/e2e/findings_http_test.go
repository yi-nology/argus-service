// findings_http_test.go —— 意见治理台路由契约（v8.5.0）：GET /findings 的
// 查询参数绑定、状态过滤、仓库维度 403、响应信封与计数。
package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/domain"
)

// seedPrint 直插一条 finding_prints 记录（可指定是否已抑制）。
func seedPrint(t *testing.T, s *stack, owner, repo, number, fp, sev string, suppressed bool) {
	t.Helper()
	ctx := context.Background()
	f := domain.Finding{File: "a.go", Line: 7, Severity: sev, Comment: "意见-" + fp, By: "correctness"}
	if _, err := s.store.UpsertPrint(ctx, "gitea", owner, repo, number, fp, f, "t-"+number, time.Now()); err != nil {
		t.Fatal(err)
	}
	if suppressed {
		if _, err := s.store.SuppressPrint(ctx, "gitea", owner, repo, number, fp, f, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFindingsHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})
	seedPrint(t, s, "myorg", "app", "7", "fp0aaaaaaaaaaaa", "high", false)
	seedPrint(t, s, "myorg", "app", "8", "fp0bbbbbbbbbbbb", "low", true)
	seedPrint(t, s, "other", "secret", "1", "fp0cccccccccccc", "medium", false)

	t.Run("list_all", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/findings?platform=gitea&owner=myorg&repo=app", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		if !strings.Contains(body, "fp0aaaaaaaaaaaa") || !strings.Contains(body, "fp0bbbbbbbbbbbb") {
			t.Fatalf("应含两条指纹: %s", body)
		}
		if !strings.Contains(body, `"total":2`) || !strings.Contains(body, `"open":1`) || !strings.Contains(body, `"suppressed":1`) {
			t.Fatalf("计数字段不符: %s", body)
		}
	})

	t.Run("status_filter", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/findings?platform=gitea&owner=myorg&repo=app&status=suppressed", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		if !strings.Contains(body, "fp0bbbbbbbbbbbb") || strings.Contains(body, "fp0aaaaaaaaaaaa") {
			t.Fatalf("suppressed 过滤不符: %s", body)
		}
	})

	t.Run("cross_repo_denied", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/findings?platform=gitea&owner=other&repo=secret", "tok-r", "")
		if code != http.StatusForbidden {
			t.Fatalf("跨仓 = %d（期望 403）", code)
		}
	})

	t.Run("missing_repo_bad_request", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/findings?platform=gitea&owner=myorg", "tok-r", "")
		if code != http.StatusBadRequest {
			t.Fatalf("缺 repo = %d（期望 400）", code)
		}
	})
}
