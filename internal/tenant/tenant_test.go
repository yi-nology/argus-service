// tenant_test.go —— tenant 基元行为钉（Normalize/ctx/Key/逃生门）。

package tenant

import (
	"context"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":        Default,
		"   ":     Default,
		"  acme ": "acme",
		"acme":    "acme",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	// 字面量钉：tenantDefault（storage 迁移）与本包同源。
	if Default != "default" {
		t.Fatalf(`Default 字面量漂移（storage tenantDefault 钉 "default"）: %q`, Default)
	}
}

func TestContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), "")
	if FromContext(ctx) != Default {
		t.Fatalf("空租户写入应归一 Default, got %q", FromContext(ctx))
	}
	ctx = NewContext(context.Background(), "acme")
	if FromContext(ctx) != "acme" {
		t.Fatalf("got %q", FromContext(ctx))
	}
	if FromContext(context.Background()) != Default {
		t.Fatal("未写入 ctx 应取 Default")
	}
	//lint:ignore SA1012 nil 入参防御是 FromContext 契约（内部路径可能传 nil），显式钉行为。
	if FromContext(nil) != Default {
		t.Fatal("nil ctx 应取 Default（防内部路径 panic）")
	}
}

func TestKey(t *testing.T) {
	if got := Key("acme", "gitea:owner/repo#7"); got != "acme/gitea:owner/repo#7" {
		t.Fatalf("got %q", got)
	}
	if got := Key("", "rest"); got != "default/rest" {
		t.Fatalf("空租户前缀应归一, got %q", got)
	}
	if got := Key("acme", ""); got != "acme" {
		t.Fatalf("空 rest 应只留租户段, got %q", got)
	}
}

func TestScopeSkipped(t *testing.T) {
	if ScopeSkipped(context.Background()) {
		t.Fatal("普通 ctx 不应跳过")
	}
	if !ScopeSkipped(WithoutScope(context.Background())) {
		t.Fatal("WithoutScope 应标记跳过")
	}
}
