package argusctx

// SDKCtx 构造与 GitHub App 装配测试：PAT/空配置/未知平台分支、GitHub App
// 启动期首取 token（HTTP 打桩）、失败仅跳过该平台、注册口与 Ready 观测面。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
)

func TestNewSDKCtxPATAndSkips(t *testing.T) {
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{
		// 常规 PAT；无 secret + SkipTLS 触发两条启动告警，但平台应就绪。
		"gitea": {Token: "t", BaseURL: "http://127.0.0.1:1", SkipTLS: true},
		// 无 token：跳过。
		"empty": {},
		// 未知平台：provider 构造失败 → 告警跳过，不阻塞启动。
		"weird": {Token: "t"},
	}}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("NewSDKCtx: %v", err)
	}
	if len(s.Ready()) != 1 || s.Ready()[0] != "gitea" {
		t.Fatalf("Ready = %v, want [gitea]（空 token/未知平台跳过）", s.Ready())
	}
	if _, err := s.Provider("gitea"); err != nil {
		t.Fatalf("gitea Provider: %v", err)
	}
	if _, err := s.Provider("empty"); err == nil {
		t.Fatal("未配置平台应报错")
	}
	if s.HasGitHubApp() {
		t.Fatal("PAT 模式不应启用 GitHub App")
	}
	if got := s.GitHubAppToken(context.Background()); got != "" {
		t.Fatalf("未启用 App 时 token 应为空, got %q", got)
	}
}

func TestNewSDKCtxEmptyPlatforms(t *testing.T) {
	s, err := NewSDKCtx(&config.Config{}, logx.Nop())
	if err != nil {
		t.Fatalf("空配置不应失败: %v", err)
	}
	if len(s.Ready()) != 0 {
		t.Fatalf("Ready 应为空: %v", s.Ready())
	}
}

func TestNewSDKCtxGitHubApp(t *testing.T) {
	_, keyPEM := rsaTestKey(t)
	stubGitHubHTTP(t, func(*http.Request) (*http.Response, error) {
		return httpJSONResponse(http.StatusCreated,
			`{"token":"ghs_app","expires_at":"2026-11-16T13:00:00Z"}`), nil
	})
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{
		"github": {
			BaseURL:     "http://127.0.0.1:1",
			GitHubAppID: 1, GitHubAppPrivateKey: keyPEM, GitHubAppInstallationID: 2,
		},
	}}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("NewSDKCtx: %v", err)
	}
	if !s.HasGitHubApp() {
		t.Fatal("三凭证齐全应启用 GitHub App")
	}
	if got := s.GitHubAppToken(context.Background()); got != "ghs_app" {
		t.Fatalf("GitHubAppToken = %q, want ghs_app", got)
	}
	if len(s.Ready()) != 1 || s.Ready()[0] != "github" {
		t.Fatalf("Ready = %v, want [github]", s.Ready())
	}
}

func TestNewSDKCtxGitHubAppBadKeySkipsPlatform(t *testing.T) {
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{
		"github": {
			BaseURL:     "http://127.0.0.1:1",
			GitHubAppID: 1, GitHubAppPrivateKey: "not-a-pem", GitHubAppInstallationID: 2,
		},
	}}
	s, err := NewSDKCtx(cfg, logx.Nop())
	if err != nil {
		t.Fatalf("坏凭据不应阻塞启动: %v", err)
	}
	if s.HasGitHubApp() {
		t.Fatal("凭据无效不应启用 App")
	}
	if len(s.Ready()) != 0 {
		t.Fatalf("该平台应判死跳过: %v", s.Ready())
	}
}

func TestRegisterProviderLowercasesAndOverrides(t *testing.T) {
	s := NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"github": &capsFake{}})
	replacement := &capsFake{}
	s.RegisterProvider("GitHub", replacement)
	got, err := s.Provider("github")
	if err != nil {
		t.Fatal(err)
	}
	if got != replacement {
		t.Fatal("RegisterProvider 应覆盖同名平台")
	}

	var zero SDKCtx
	zero.RegisterProvider("gitea", &capsFake{})
	if _, err := zero.Provider("gitea"); err != nil {
		t.Fatalf("零值 SDKCtx 注册后应可查: %v", err)
	}
	if got := zero.Ready(); len(got) != 1 || !strings.EqualFold(got[0], "gitea") {
		t.Fatalf("Ready = %v", got)
	}
}
