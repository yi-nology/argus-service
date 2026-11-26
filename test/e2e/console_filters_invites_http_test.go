// console_filters_invites_http_test.go —— v8.8.2 控制台两面契约：
// GET /tasks 筛选参数（kind/repo/q/跨仓）与 GET /auth/invites/history（admin）。
package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/storage"
)

// seedTask 直插任务（仓储层；确定性 id/head 便于关键词断言）。
func seedTask(t *testing.T, s *stack, id, kind, owner, repo, number, head, status string) {
	t.Helper()
	task := storage.NewTask(kind, "gitea", owner, repo, number)
	task.ID = id
	task.HeadSHA = head
	task.Status = status
	if err := s.store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestTaskListFiltersHTTP(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})
	seedTask(t, s, "t-filter-0001", "code_review", "myorg", "app", "1", "headaaa1", storage.StatusDone)
	seedTask(t, s, "t-filter-0002", "describe", "myorg", "app", "2", "headbbb2", storage.StatusPending)
	seedTask(t, s, "t-filter-0003", "code_review", "other", "secret", "3", "headccc3", storage.StatusDone)

	t.Run("kind_filter", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/tasks?kind=describe", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		if !strings.Contains(body, "t-filter-0002") || strings.Contains(body, "t-filter-0001") {
			t.Fatalf("kind 过滤不符: %s", body)
		}
	})

	t.Run("repo_and_q_filter", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/tasks?owner=myorg&repo=app&q=headbbb", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		if !strings.Contains(body, "t-filter-0002") || strings.Contains(body, "t-filter-0001") {
			t.Fatalf("仓库+关键词过滤不符: %s", body)
		}
	})

	t.Run("cross_repo_excluded", func(t *testing.T) {
		code, body := authzDo(t, s, http.MethodGet, "/tasks?owner=other", "tok-r", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		if strings.Contains(body, "t-filter-0003") {
			t.Fatalf("跨仓任务不应出现: %s", body)
		}
	})

	t.Run("invalid_status_400", func(t *testing.T) {
		code, _ := authzDo(t, s, http.MethodGet, "/tasks?status=bogus", "tok-r", "")
		if code != http.StatusBadRequest {
			t.Fatalf("非法状态 code=%d（期望 400）", code)
		}
	})
}

func TestInviteHistoryHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})
	ctx := context.Background()
	// 待接受邀请 + 已过期邀请（ttl 负数 = 立即过期）。
	if _, _, err := s.store.Auths().CreateInvite(ctx, storage.RoleViewer, "", "seed", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.store.Auths().CreateInvite(ctx, storage.RoleMaintainer, "bob", "seed", -time.Minute); err != nil {
		t.Fatal(err)
	}

	code, body := authzDo(t, s, http.MethodGet, "/auth/invites/history", "tok-r", "")
	if code != http.StatusForbidden || !strings.Contains(body, "仅 admin") {
		t.Fatalf("read 档 code=%d body=%s（期望 403 可读原因）", code, body)
	}

	if _, err := s.store.Auths().SeedBootstrapAdmin(ctx, "boss", "pass-1234"); err != nil {
		t.Fatal(err)
	}
	code, body = postJSON(t, s.baseURL+"/auth/login", `{"username":"boss","password":"pass-1234"}`, "")
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%s", code, body)
	}
	tok := jsonField(t, body, "token")
	code, body = authzDo(t, s, http.MethodGet, "/auth/invites/history", tok, "")
	if code != http.StatusOK {
		t.Fatalf("admin code=%d body=%s", code, body)
	}
	for _, want := range []string{`"pending"`, `"expired"`, `"bob"`, `"viewer"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("邀请历史缺 %s: %s", want, body)
		}
	}
	if strings.Contains(body, "token") {
		t.Fatalf("邀请历史不得含 token 字段: %s", body)
	}
}
