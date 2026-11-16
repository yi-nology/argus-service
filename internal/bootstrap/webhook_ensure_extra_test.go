package bootstrap

// ensureWebhooks 的跳过/失败分支：平台不可用、缺 token、事件词表缺失、
// 非冲突失败都必须计数并继续处理后续仓库（不因单仓失败中断整轮）。

import (
	"context"
	"errors"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/poller"
)

func TestEnsureWebhooksSkipsAndFailsBranches(t *testing.T) {
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{
			// 缺 token：跳过
			"gitea": {Secret: "s"},
			// token/secret 齐但无默认事件词表：跳过
			"custom": {Token: "t", Secret: "s"},
		},
	}
	targets := []poller.Target{
		{Platform: "missing-platform", Owner: "o", Repo: "r"}, // provider 不可用
		{Platform: "gitea", Owner: "o", Repo: "r"},            // 缺 token
		{Platform: "custom", Owner: "o", Repo: "r"},           // 无事件词表
	}
	gitea, custom := &hookFake{}, &hookFake{}
	sum := ensureWebhooks(context.Background(),
		stubSDK{"gitea": gitea, "custom": custom}, cfg, logx.Nop(), targets)
	if sum.failed != 1 {
		t.Errorf("平台不可用 failed = %d, want 1", sum.failed)
	}
	if gitea.createN != 0 || custom.createN != 0 {
		t.Errorf("缺 token/词表分支不应发起创建: gitea=%d custom=%d", gitea.createN, custom.createN)
	}
	if sum.created != 0 || sum.updated != 0 || sum.unchanged != 0 || sum.conflict != 0 {
		t.Errorf("跳过分支不应产生动作: %+v", sum)
	}
}

func TestEnsureWebhooksNonConflictFailureCounted(t *testing.T) {
	f := &hookFake{
		existing: []*provider.PlatformWebhook{
			{ID: 7, URL: "http://argus:8899/webhook/gitea",
				Events: []string{"pull_request_only", "issue_comment"}},
		},
		updateErr: errors.New("platform 500"),
	}
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{"gitea": {Token: "t", Secret: "s"}},
	}
	sum := ensureWebhooks(context.Background(), stubSDK{"gitea": f}, cfg, logx.Nop(),
		[]poller.Target{{Platform: "gitea", Owner: "o", Repo: "r"}})
	if sum.failed != 1 || sum.conflict != 0 {
		t.Fatalf("非冲突失败应计 failed 不计 conflict: %+v", sum)
	}
	if f.updateN != 1 {
		t.Fatalf("应尝试 secret patch 一次, got %d", f.updateN)
	}
}

func TestEnsureWebhooksContinuesAfterFailure(t *testing.T) {
	// 第一仓失败不阻断第二仓：两个平台各自独立处理。
	f := &hookFake{updateErr: errors.New("boom"),
		existing: []*provider.PlatformWebhook{
			{ID: 1, URL: "http://argus:8899/webhook/gitea",
				Events: []string{"pull_request_only", "issue_comment"}},
		}}
	okFake := &hookFake{}
	cfg := &config.Config{
		PublicURL: "http://argus:8899",
		Platforms: map[string]config.PlatformConfig{
			"gitea":  {Token: "t", Secret: "s"},
			"github": {Token: "t", Secret: "s"},
		},
	}
	sum := ensureWebhooks(context.Background(), stubSDK{"gitea": f, "github": okFake},
		cfg, logx.Nop(), []poller.Target{
			{Platform: "gitea", Owner: "o", Repo: "r1"},
			{Platform: "github", Owner: "o", Repo: "r2"},
		})
	if sum.failed != 1 || sum.created != 1 {
		t.Fatalf("失败后应继续创建下一仓: %+v", sum)
	}
	if okFake.createN != 1 {
		t.Errorf("github 应创建 1 次, got %d", okFake.createN)
	}
}
