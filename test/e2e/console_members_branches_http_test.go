// console_members_branches_http_test.go —— v8.8.0 两新面路由契约：
// GET /auth/members（admin 面，read 档 403；邀请链建号后可查）
// GET /repos/branches（read 档；未装配平台 400、无 BranchManager 502 可读原因）
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// postJSON 裸 JSON POST（登录等公开面用；带 token 时走 Bearer）。
func postJSON(t *testing.T, url, body, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// jsonField 取信封 data.<field> 字符串（测试契约固定，缺字段即失败）。
func jsonField(t *testing.T, body, field string) string {
	t.Helper()
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("解析信封失败: %v body=%s", err, body)
	}
	v, ok := env.Data[field].(string)
	if !ok || v == "" {
		t.Fatalf("信封缺 data.%s: %s", field, body)
	}
	return v
}

func TestMembersHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})

	// read 档 → 403（admin 管理面）
	code, body := authzDo(t, s, http.MethodGet, "/auth/members", "tok-r", "")
	if code != http.StatusForbidden {
		t.Fatalf("read 档 code=%d body=%s（期望 403）", code, body)
	}
	if !strings.Contains(body, "仅 admin") {
		t.Fatalf("403 应带可读原因: %s", body)
	}

	// 邀请链建号 + 登录 → 会话 admin 可查清单
	if _, err := s.store.Auths().SeedBootstrapAdmin(context.Background(), "boss", "pass-1234"); err != nil {
		t.Fatal(err)
	}
	code, body = postJSON(t, s.baseURL+"/auth/login", `{"username":"boss","password":"pass-1234"}`, "")
	if code != http.StatusOK {
		t.Fatalf("login code=%d body=%s", code, body)
	}
	tok := jsonField(t, body, "token")
	code, body = authzDo(t, s, http.MethodGet, "/auth/members", tok, "")
	if code != http.StatusOK {
		t.Fatalf("admin code=%d body=%s", code, body)
	}
	if !strings.Contains(body, `"boss"`) || !strings.Contains(body, `"role"`) {
		t.Fatalf("成员清单缺字段: %s", body)
	}
	if strings.Contains(body, "password") || strings.Contains(body, "salt") {
		t.Fatalf("成员清单不得含凭证字段: %s", body)
	}
}

func TestRepoBranchesHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})

	// 未装配平台 → 400 且透出具体原因（reply errDetail）
	code, body := authzDo(t, s, http.MethodGet, "/repos/branches?platform=tencent_code&owner=myorg&repo=app", "tok-r", "")
	if code != http.StatusBadRequest {
		t.Fatalf("未装配平台 code=%d body=%s（期望 400）", code, body)
	}
	if !strings.Contains(body, "未在部署装配") {
		t.Fatalf("400 应带可读原因: %s", body)
	}

	// 正常仓 → 200：分支名升序 + 默认分支（GetRepo best-effort）
	code, body = authzDo(t, s, http.MethodGet, "/repos/branches?platform=gitea&owner=myorg&repo=app", "tok-r", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s（期望 200）", code, body)
	}
	for _, want := range []string{`"branches"`, `"main"`, `"release/1.2"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("分支清单缺 %s: %s", want, body)
		}
	}

	// 跨仓（token 约束外）→ 403
	code, _ = authzDo(t, s, http.MethodGet, "/repos/branches?platform=gitea&owner=other&repo=secret", "tok-r", "")
	if code != http.StatusForbidden {
		t.Fatalf("跨仓 code=%d（期望 403）", code)
	}
}

// TestRepoCRsHTTPContract v8.8.1 PR 号下拉数据面：成功清单（默认 opened）+
// 未装配平台 400 + 状态词表校验 400 + 跨仓 403。
func TestRepoCRsHTTPContract(t *testing.T) {
	s := newStack(t, stackOpts{apiTokens: "tok-r:read@gitea:myorg/*"})

	code, body := authzDo(t, s, http.MethodGet, "/repos/crs?platform=gitea&owner=myorg&repo=app", "tok-r", "")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s（期望 200）", code, body)
	}
	for _, want := range []string{`"items"`, `"number":"7"`, `"title"`, `"alice"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("PR 清单缺 %s: %s", want, body)
		}
	}

	code, body = authzDo(t, s, http.MethodGet, "/repos/crs?platform=tencent_code&owner=myorg&repo=app", "tok-r", "")
	if code != http.StatusBadRequest || !strings.Contains(body, "未在部署装配") {
		t.Fatalf("未装配平台 code=%d body=%s（期望 400 可读原因）", code, body)
	}

	code, body = authzDo(t, s, http.MethodGet, "/repos/crs?platform=gitea&owner=myorg&repo=app&state=bogus", "tok-r", "")
	if code != http.StatusBadRequest || !strings.Contains(body, "state 只支持") {
		t.Fatalf("非法状态 code=%d body=%s（期望 400）", code, body)
	}

	code, _ = authzDo(t, s, http.MethodGet, "/repos/crs?platform=gitea&owner=other&repo=secret", "tok-r", "")
	if code != http.StatusForbidden {
		t.Fatalf("跨仓 code=%d（期望 403）", code)
	}
}
