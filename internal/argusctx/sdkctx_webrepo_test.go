package argusctx

// webhook delivery/解析与仓库配置加载：delivery header 顺序、secret 透传、
// 仓库配置缺失/坏 YAML 的降级语义（敌对输入不炸任务）。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/yi-nology/go-git-platform/provider"
)

func TestExtractDeliveryID(t *testing.T) {
	s := NewSDKCtxStubbed(nopLogx{}, nil)

	headers := map[string]string{"X-GitHub-Delivery": "gh-1"}
	if got := s.ExtractDeliveryID(func(k string) string { return headers[k] }); got != "gh-1" {
		t.Fatalf("GitHub delivery = %q, want gh-1", got)
	}

	// 顺序固定：Gitea 优先于 GitHub。
	headers = map[string]string{
		"X-Gitea-Delivery":    "gitea-1",
		"X-GitHub-Delivery":   "gh-2",
		"X-Gitlab-Event-UUID": "gl-1",
	}
	if got := s.ExtractDeliveryID(func(k string) string { return headers[k] }); got != "gitea-1" {
		t.Fatalf("delivery 优先序错: %q", got)
	}

	if got := s.ExtractDeliveryID(func(string) string { return "" }); got != "" {
		t.Fatalf("无 header 应返回空串, got %q", got)
	}
}

func TestParseWebhookDelegatesToProvider(t *testing.T) {
	event := &provider.NormalizedEvent{ID: "evt-1", Type: "cr.opened"}
	f := &capsFake{webhookEvent: event}
	s := capsStub(f)
	req, err := http.NewRequest(http.MethodPost, "http://argus/webhook/github", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ParseWebhook("github", req, "s3cret")
	if err != nil || got == nil || got.ID != "evt-1" || got.Type != "cr.opened" {
		t.Fatalf("ParseWebhook = %+v, %v", got, err)
	}

	// 未配置平台：Provider 解析错误上抛（不静默吞）。
	if _, err := s.ParseWebhook("unknown", req, "s"); err == nil {
		t.Fatal("未配置平台应报错")
	}

	// provider 解析错误（签名拒绝等）原样上抛。
	f2 := &capsFake{webhookErr: errors.New("signature mismatch")}
	if _, err := capsStub(f2).ParseWebhook("github", req, "s"); err == nil ||
		!strings.Contains(err.Error(), "signature mismatch") {
		t.Fatalf("provider 错误应上抛, got %v", err)
	}
}

func TestLoadRepoConfigMissingFileReturnsEmpty(t *testing.T) {
	f := &capsFake{fileErr: fmt.Errorf("get file: %w", provider.ErrNotFound)}
	rc, err := capsStub(f).LoadRepoConfig(context.Background(), "github", "o", "r", "main")
	if err != nil {
		t.Fatalf("404 应降级为空配置: %v", err)
	}
	if rc == nil || rc.Loaded {
		t.Fatalf("缺文件应返回 EmptyRepoConfig: %+v", rc)
	}
	if f.fileN != 1 || f.lastFileRef != "main" {
		t.Fatalf("读取凭据错: n=%d ref=%q", f.fileN, f.lastFileRef)
	}
}

func TestLoadRepoConfigValidAndInvalid(t *testing.T) {
	f := &capsFake{fileBody: "use:\n  - security\n"}
	rc, err := capsStub(f).LoadRepoConfig(context.Background(), "github", "o", "r", "main")
	if err != nil || rc == nil || !rc.Loaded {
		t.Fatalf("合法配置应 Loaded: %+v %v", rc, err)
	}
	if len(rc.Use) != 1 || rc.Use[0] != "security" {
		t.Fatalf("Use = %v", rc.Use)
	}

	// 敌对输入：坏 YAML 按无配置处理（不炸任务），且不返回半解析结果。
	f2 := &capsFake{fileBody: "use: [unclosed\n"}
	rc, err = capsStub(f2).LoadRepoConfig(context.Background(), "github", "o", "r", "main")
	if err != nil {
		t.Fatalf("坏 YAML 不应上抛: %v", err)
	}
	if rc == nil || rc.Loaded {
		t.Fatalf("坏 YAML 应降级空配置: %+v", rc)
	}

	// 非 404 的读取错误：上抛（调用方决定是否降级）。
	f3 := &capsFake{fileErr: errors.New("platform 500")}
	if _, err := capsStub(f3).LoadRepoConfig(context.Background(), "github", "o", "r", "main"); err == nil {
		t.Fatal("500 应上抛")
	}
}

func TestLoadRepoConfigAutoResolvesDefaultBranch(t *testing.T) {
	f := &capsFake{
		repo:     &provider.PlatformRepo{DefaultBranch: "develop"},
		fileBody: "use: []\n",
	}
	rc, err := capsStub(f).LoadRepoConfigAuto(context.Background(), "github", "o", "r", "")
	if err != nil || rc == nil {
		t.Fatalf("LoadRepoConfigAuto: %+v %v", rc, err)
	}
	if f.repoN != 1 {
		t.Fatalf("应解析默认分支一次, got %d", f.repoN)
	}
	if f.lastFileRef != "develop" {
		t.Fatalf("仓库配置应读默认分支, ref=%q", f.lastFileRef)
	}
}
