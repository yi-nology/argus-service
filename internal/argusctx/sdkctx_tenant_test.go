package argusctx

import (
	"context"
	"net/http"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/tenant"
)

type fakeProv struct {
	provider.Provider
	name string
}

// TestProviderForTenantFallback 租户感知平台客户端（#90）：命中租户覆盖用
// 租户客户端；未配置/未覆盖回落全局。
func TestProviderForTenantFallback(t *testing.T) {
	s := &SDKCtx{provs: map[string]provider.Provider{"gitea": fakeProv{name: "global"}},
		tenantProvs: map[string]provider.Provider{"tenant-a/gitea": fakeProv{name: "tenant-a"}}}
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	p, err := s.ProviderFor(ctxA, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if fp, ok := p.(fakeProv); !ok || fp.name != "tenant-a" {
		t.Fatalf("应命中租户覆盖: %+v", p)
	}
	// default 租户回落全局。
	p, err = s.ProviderFor(context.Background(), "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if fp, ok := p.(fakeProv); !ok || fp.name != "global" {
		t.Fatalf("default 应回落全局: %+v %v", p, err)
	}
	// 租户未配该平台 → 回落全局。
	if _, err = s.ProviderFor(ctxA, "gitlab"); err == nil {
		t.Fatal("未接入平台应报错")
	}
}

// TestNewSDKCtxTenantPlatforms 租户平台凭证装配（#90）：TenantProfiles 配了
// platforms 的租户启动期构造独立 provider，ProviderFor 按 ctx 租户命中。
func TestNewSDKCtxTenantPlatforms(t *testing.T) {
	cfg := &config.Config{
		Platforms: map[string]config.PlatformConfig{
			"gitea": {Token: "global-tok", BaseURL: "http://127.0.0.1:1"},
		},
		TenantProfiles: map[string]config.TenantProfile{
			"tenant-a": {Platforms: map[string]config.PlatformConfig{
				"gitea": {Token: "tenant-tok", BaseURL: "http://127.0.0.1:1"},
			}},
			// 无 token 条目跳过；未知平台失败仅告警。
			"tenant-b": {Platforms: map[string]config.PlatformConfig{"weird": {Token: "x"}}},
		},
	}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("NewSDKCtx: %v", err)
	}
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	p, err := s.ProviderFor(ctxA, "gitea")
	if err != nil {
		t.Fatalf("租户 provider: %v", err)
	}
	if p == nil {
		t.Fatal("tenant-a 应有独立 provider")
	}
	// 未配租户覆盖的平台回落全局。
	if _, err := s.ProviderFor(tenant.NewContext(context.Background(), "tenant-c"), "gitea"); err != nil {
		t.Fatalf("未覆盖租户应回落全局: %v", err)
	}
}

// TestNewSDKCtxTenantGitHubApp #107：租户 GitHub App——profile 平台配 App
// 三件套时租户 provider 走 installation 认证（独立于全局 appTokens）。
func TestNewSDKCtxTenantGitHubApp(t *testing.T) {
	_, keyPEM := rsaTestKey(t)
	stubGitHubHTTP(t, func(*http.Request) (*http.Response, error) {
		return httpJSONResponse(http.StatusCreated,
			`{"token":"ghs_tenant","expires_at":"2026-11-16T13:00:00Z"}`), nil
	})
	cfg := &config.Config{
		Platforms: map[string]config.PlatformConfig{
			"github": {Token: "global-pat", BaseURL: "http://127.0.0.1:1"},
		},
		TenantProfiles: map[string]config.TenantProfile{
			"tenant-a": {Platforms: map[string]config.PlatformConfig{
				"github": {
					BaseURL:     "http://127.0.0.1:1",
					GitHubAppID: 7, GitHubAppPrivateKey: keyPEM, GitHubAppInstallationID: 9,
				},
			}},
		},
	}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("NewSDKCtx: %v", err)
	}
	// 全局仍是 PAT 形态（HasGitHubApp false），租户 provider 独立存在。
	if s.HasGitHubApp() {
		t.Fatal("全局未配 App 不应启用全局 App")
	}
	ctxA := tenant.NewContext(context.Background(), "tenant-a")
	if _, err := s.ProviderFor(ctxA, "github"); err != nil {
		t.Fatalf("租户 App provider: %v", err)
	}
	// 未覆盖租户回落全局 PAT provider。
	if _, err := s.ProviderFor(context.Background(), "github"); err != nil {
		t.Fatalf("全局 provider: %v", err)
	}
}
