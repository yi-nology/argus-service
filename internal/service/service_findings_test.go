// service_findings_test.go —— 意见治理台读面（v8.5.0）：入参校验、仓库维度
// 授权、状态过滤、limit 钳制与响应计数。
package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/storage"
)

func newFindingsSvc(t *testing.T) *Service {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "findings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{
		Store: st,
		Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
			"gitea": {Token: "t"},
		}},
		Log: testLogger{},
	}
}

func TestListFindings(t *testing.T) {
	svc := newFindingsSvc(t)
	ctx := ContextWithScope(context.Background(), "read")
	at := time.Now()
	f := domain.Finding{File: "a.go", Line: 3, Severity: "high", Comment: "x", By: "correctness"}
	if _, err := svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "7", "fp0aaaaaaaaaaaa", f, "t1", at); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.UpsertPrint(ctx, "gitea", "acme", "app", "8", "fp0bbbbbbbbbbbb", f, "t2", at); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Store.SuppressPrint(ctx, "gitea", "acme", "app", "7", "fp0aaaaaaaaaaaa", f, at); err != nil {
		t.Fatal(err)
	}

	// 全部：2 条 + 计数
	got, code := svc.ListFindings(ctx, "gitea", "acme", "app", "", 0)
	if code != 200 {
		t.Fatalf("全部清单 code=%d body=%v", code, got)
	}
	items, _ := got["findings"].([]map[string]any)
	if len(items) != 2 {
		t.Fatalf("findings 应 2 条: %+v", got)
	}
	counts, _ := got["counts"].(map[string]int64)
	if counts["total"] != 2 || counts["open"] != 1 || counts["suppressed"] != 1 {
		t.Fatalf("counts 不符: %+v", counts)
	}
	if got["limit"] != defaultListFindingsLimit {
		t.Fatalf("limit 缺省应 %d, got %v", defaultListFindingsLimit, got["limit"])
	}
	// 状态过滤
	got, code = svc.ListFindings(ctx, "gitea", "acme", "app", storage.PrintSuppressed, 0)
	if code != 200 {
		t.Fatalf("suppressed 过滤 code=%d", code)
	}
	if items, _ = got["findings"].([]map[string]any); len(items) != 1 || items[0]["status"] != storage.PrintSuppressed {
		t.Fatalf("suppressed 过滤应 1 条: %+v", got)
	}
	// limit 上限钳制
	got, _ = svc.ListFindings(ctx, "gitea", "acme", "app", "", 9999)
	if got["limit"] != maxListFindingsLimit {
		t.Fatalf("limit 应钳制到 %d, got %v", maxListFindingsLimit, got["limit"])
	}
}

func TestListFindingsValidationAndAuthz(t *testing.T) {
	svc := newFindingsSvc(t)
	ctx := ContextWithScope(context.Background(), "read")

	cases := []struct {
		name, platform, owner, repo, status string
		want                                int
	}{
		{"platform 未装配", "nope", "acme", "app", "", 400},
		{"owner 空", "gitea", "", "app", "", 400},
		{"repo 非法字符", "gitea", "acme", "app@evil", "", 400},
		{"status 词表外", "gitea", "acme", "app", "bogus", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, code := svc.ListFindings(ctx, tc.platform, tc.owner, tc.repo, tc.status, 0); code != tc.want {
				t.Fatalf("code=%d, want %d", code, tc.want)
			}
		})
	}

	// 仓库维度：约束在 myorg/* 的读 token 不得读 other/secret
	scoped := ContextWithTokenRepos(ContextWithScope(context.Background(), "read"), "gitea:myorg/*")
	if _, code := svc.ListFindings(scoped, "gitea", "other", "secret", "", 0); code != 403 {
		t.Fatalf("跨仓读取应 403, got %d", code)
	}
	if _, code := svc.ListFindings(scoped, "gitea", "myorg", "app", "", 0); code != 200 {
		t.Fatalf("约束内读取应 200, got %d", code)
	}
}
