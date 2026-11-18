package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTenantProfiles(t *testing.T) {
	// 空路径 = 空表。
	if got := LoadTenantProfiles(""); got != nil {
		t.Fatalf("空路径应 nil: %+v", got)
	}
	// 坏 JSON 留痕回落。
	bad := filepath.Join(t.TempDir(), "p.json")
	_ = os.WriteFile(bad, []byte("{oops"), 0o644)
	if got := LoadTenantProfiles(bad); got != nil {
		t.Fatalf("坏 JSON 应回落 nil: %+v", got)
	}
	// 正常装载 + 非法 ID 剔除。
	good := filepath.Join(t.TempDir(), "p.json")
	raw := `{"tenants":{"tenant-a":{"bot_username":"bot-a","sink":{"url":"http://x"},"real_token_budget":500000},"bad id":{},"x/y":{}}}`
	_ = os.WriteFile(good, []byte(raw), 0o644)
	got := LoadTenantProfiles(good)
	if len(got) != 1 {
		t.Fatalf("非法 ID 应剔除，剩 1 个: %+v", got)
	}
	p := got["tenant-a"]
	if p.BotUsername != "bot-a" || p.Sink.URL != "http://x" || p.RealTokenBudget != 500000 {
		t.Fatalf("profile 解析错: %+v", p)
	}
	// ProfileFor resolver。
	c := &Config{TenantProfiles: got}
	if c.ProfileFor("tenant-a").BotUsername != "bot-a" {
		t.Fatal("ProfileFor 应命中")
	}
	if (&Config{}).ProfileFor("a").BotUsername != "" || c.ProfileFor("nope").BotUsername != "" {
		t.Fatal("未命中应零值 profile")
	}
}

func TestNormalizeTenantID(t *testing.T) {
	ok := []string{"a", "tenant-1", "ACME"}
	for _, id := range ok {
		if NormalizeTenantID(id) == "" {
			t.Errorf("%q 应合法", id)
		}
	}
	bad := []string{"", "a b", "a/b", "a\\b", "a\tb"}
	for _, id := range bad {
		if NormalizeTenantID(id) != "" {
			t.Errorf("%q 应拒绝", id)
		}
	}
}
