// repo_settings_wildcard_test.go —— GitLab 子组仓库名（repo 含 "/"）回归：
// :repo 单段路由在 %2F 被 hertz 解码为 "/" 后匹配失败 → 43 实测 GET/PUT/DELETE
// 全 404，按仓配置页对 ops/deploy-kit 之类仓库不可用。路由改 /*repo 通配后，
// 单段与多段仓库名必须同样工作（读写删闭环）。
package e2e

import (
	"net/http"
	"strings"
	"testing"
)

func TestRepoSettingNestedRepoPath(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok:admin"})
	const path = "/repo-settings/gitea/myorg/ops%2Fdeploy-kit"

	code, body := authzDo(t, s, http.MethodGet, path, "tok", "")
	if code != http.StatusOK {
		t.Fatalf("嵌套仓库 GET = %d（期望 200；修复前 :repo 单段路由 404）body=%s", code, body)
	}
	if !strings.Contains(body, `"found":false`) {
		t.Fatalf("初始应无覆盖: %s", body)
	}

	code, body = authzDo(t, s, http.MethodPut, path, "tok", `{"comment_throttle_minutes":5}`)
	if code != http.StatusOK {
		t.Fatalf("嵌套仓库 PUT = %d body=%s", code, body)
	}

	code, body = authzDo(t, s, http.MethodGet, path, "tok", "")
	if code != http.StatusOK {
		t.Fatalf("写入后 GET = %d body=%s", code, body)
	}
	if !strings.Contains(body, `"comment_throttle_minutes":5`) {
		t.Fatalf("覆盖未生效: %s", body)
	}

	code, body = authzDo(t, s, http.MethodDelete, path, "tok", "")
	if code != http.StatusOK {
		t.Fatalf("嵌套仓库 DELETE = %d body=%s", code, body)
	}
	code, body = authzDo(t, s, http.MethodGet, path, "tok", "")
	if code != http.StatusOK || !strings.Contains(body, `"found":false`) {
		t.Fatalf("删除后应回到无覆盖: %d %s", code, body)
	}
}
