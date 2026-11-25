// console_options_http_test.go —— 控制台选项聚合面路由契约（v8.7.0）：
// GET /console/options 词表四件透出 + repos 行过滤 + 信封。
package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

func TestConsoleOptionsHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})
	ctx := tenant.WithoutScope(context.Background())
	for _, row := range []storage.TenantRepo{
		{TenantID: "", Platform: "gitea", Owner: "myorg", Repo: "app"},
		{TenantID: "", Platform: "gitea", Owner: "other", Repo: "secret"},
	} {
		if err := s.store.UpsertTenantRepo(ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	code, body := authzDo(t, s, http.MethodGet, "/console/options", "tok-r", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	// 四件词表 + kinds 条目 + 约束内仓库
	for _, want := range []string{`"platforms"`, `"kinds"`, `"statuses"`, `"repos"`,
		`"code_review"`, `"myorg"`, `"app"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺 %s: %s", want, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("约束外仓库漏出: %s", body)
	}
}
