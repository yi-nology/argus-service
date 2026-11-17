// webhook_tenant_test.go —— webhook 入站租户闸/白名单门/限速（#82）。

package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/tenant"
)

// newWebhookTestService 内存 SQLite 存储 + 最小 Service（webhook 闸三测共用）。
// Store 为 nil 的形态留给纯判定测试（RepoAllowed）；需要存储的用本 helper。
func newWebhookTestService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	st, err := storage.Open("file:" + filepath.Join(t.TempDir(), "wh.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{Cfg: &config.Config{}, Store: st, Log: testLogger{}}, context.Background()
}

// storageTenantRepo 造映射行（免测试文件展开 storage 包类型细节）。
func storageTenantRepo(platform, owner, repo, tid string) storage.TenantRepo {
	return storage.TenantRepo{TenantID: tid, Platform: platform, Owner: owner, Repo: repo}
}

// TestWebhookTenantOf_MappedAndDefault 坐标映射反查：登记 → 租户；未登记 →
// default（存量部署隐式归属，零行为变化）。
func TestWebhookTenantOf_MappedAndDefault(t *testing.T) {
	s, ctx := newWebhookTestService(t)
	ctxA := tenant.NewContext(ctx, "tenant-a")
	if err := s.Store.UpsertTenantRepo(ctxA, storageTenantRepo("gitea", "acme", "web", "tenant-a")); err != nil {
		t.Fatal(err)
	}
	if got := s.webhookTenantOf(ctx, "gitea", "acme", "web"); got != "tenant-a" {
		t.Fatalf("登记坐标应归 tenant-a, got %q", got)
	}
	if got := s.webhookTenantOf(ctx, "gitea", "other", "repo"); got != tenant.Default {
		t.Fatalf("未登记坐标应归 default, got %q", got)
	}
	// 映射不串租户：default 上下文也反查得到同一映射（路由表跨租户可见）。
	if got := s.webhookTenantOf(context.Background(), "gitea", "acme", "web"); got != "tenant-a" {
		t.Fatalf("路由表应跨 ctx 可见, got %q", got)
	}
}

// TestWebhookRepoAllowed 全局白名单门（#82：清单外坐标拒绝建任务）。
func TestWebhookRepoAllowed(t *testing.T) {
	s, _ := newWebhookTestService(t)
	s.Cfg.RepoAllowlist = []string{"gitlab:obs/*", "gitea:acme/web"}
	cases := map[string]bool{
		"gitea:acme/web":     true,
		"gitlab:obs/api":     true,
		"gitea:acme/other":   false,
		"github:acme/web":    false,
	}
	for repo, want := range cases {
		if got := s.webhookRepoAllowed(repo); got != want {
			t.Errorf("webhookRepoAllowed(%q) = %v, want %v", repo, got, want)
		}
	}
	// 空白名单：判定器本身恒 false，放行语义在调用门（len>0 才查）——
	// 兼容形态由 admission 的 len(RepoAllowlist)>0 前置条件承担。
	s.Cfg.RepoAllowlist = nil
	if s.webhookRepoAllowed("gitea:any/thing") {
		t.Error("空清单下判定器应恒 false（放行语义在调用门 len>0 前置）")
	}
}

// TestWebhookRateOK per-repo 限速闸：限速开启时同 key 连打超限、异 key 不互压。
func TestWebhookRateOK(t *testing.T) {
	s, _ := newWebhookTestService(t)
	s.Cfg.WebhookRateRPM = 600 // burst = max(10, 100) —— 单测窗口内可控
	if !s.webhookRateOK("gitea:a/b") {
		t.Fatal("首请求应放行")
	}
	// 同 key 灌满 burst（100）。
	for i := 0; i < 99; i++ {
		s.webhookRateOK("gitea:a/b")
	}
	if s.webhookRateOK("gitea:a/b") {
		t.Fatal("同 key 超 burst 应限流")
	}
	if !s.webhookRateOK("gitea:c/d") {
		t.Fatal("异 key 不应互压（per-repo 桶）")
	}
	// 0 = 关闭。
	s2, _ := newWebhookTestService(t)
	s2.Cfg.WebhookRateRPM = 0
	if !s2.webhookRateOK("gitea:a/b") {
		t.Fatal("限速关闭应恒放行")
	}
}
