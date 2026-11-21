package egress

import (
	"strings"
	"testing"
)

func TestIsLoopbackURL(t *testing.T) {
	cases := map[string]bool{
		"":                          true,
		"http://127.0.0.1:8080/v1":  true,
		"http://localhost:11434":    true,
		"http://[::1]:8080":         true,
		"https://api.openai.com/v1": false,
		"http://10.0.0.5/v1":        false,
		"not a url":                 false,
	}
	for in, want := range cases {
		if got := IsLoopbackURL(in); got != want {
			t.Errorf("IsLoopbackURL(%q)=%v want %v", in, got, want)
		}
	}
}

func TestExternalAllowedModes(t *testing.T) {
	external := "https://api.openai.com/v1"
	loop := "http://127.0.0.1:11434"

	// loopback 恒放行
	for _, mode := range []string{ConsentAllow, ConsentDeny, ConsentRequire, ConsentYes} {
		if ok, _ := ExternalAllowed(mode, loop); !ok {
			t.Errorf("loopback should pass mode=%s", mode)
		}
	}

	if ok, _ := ExternalAllowed(ConsentAllow, external); !ok {
		t.Error("allow mode should permit external")
	}
	if ok, reason := ExternalAllowed(ConsentDeny, external); ok || reason == "" {
		t.Errorf("deny should block external: ok=%v reason=%q", ok, reason)
	}
	// require 无显式 yes → 拦（env 未设）
	t.Setenv(ConsentEnv, "")
	if ok, reason := ExternalAllowed(ConsentRequire, external); ok || reason == "" {
		t.Errorf("require without yes should block: ok=%v reason=%q", ok, reason)
	}
	t.Setenv(ConsentEnv, ConsentYes)
	if ok, _ := ExternalAllowed(ConsentRequire, external); !ok {
		t.Error("require+yes should allow external")
	}
	if ok, _ := ExternalAllowed(ConsentYes, external); !ok {
		t.Error("yes mode should allow")
	}
}

func TestMaskCredentialsRegex(t *testing.T) {
	cases := []struct{ in, notContains string }{
		{"key=AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
		{"token ghp_abcdefghijklmnopqrstuv", "ghp_abcdefghijklmnopqrstuv"},
		{"Authorization: Bearer somelongtokenvalue1234567890", "somelongtokenvalue1234567890"},
		{"sk-" + strings.Repeat("a", 40), "sk-" + strings.Repeat("a", 40)},
	}
	for _, tc := range cases {
		out := MaskCredentials(tc.in)
		if strings.Contains(out, tc.notContains) {
			t.Errorf("MaskCredentials did not redact %q → %s", tc.notContains, out)
		}
		if !strings.Contains(out, "[REDACTED:") {
			t.Errorf("expected REDACTED marker in %s", out)
		}
	}
	// 普通文本不动
	plain := "the quick brown fox jumps over the lazy dog"
	if got := MaskCredentials(plain); got != plain {
		t.Errorf("plain text changed: %s", got)
	}
}

func TestMaskCredentialsEntropy(t *testing.T) {
	// 高熵随机串（含数字）应被掩码
	secret := "a8F3kQ9zR2mN7pL4wX6yB1cD5eG8hJ0sT"
	if len(secret) < 32 {
		// pad
		secret = secret + "uV2"
	}
	if len(secret) < 32 {
		t.Skip("need ≥32")
	}
	out := MaskCredentials("api_secret=" + secret)
	if strings.Contains(out, secret) {
		t.Errorf("high-entropy secret not masked: %s", out)
	}
	// 低熵长词（无数字或低熵）不动 —— 英文句子 token
	// 32+ 字母无数字：looksLikeSecret 要求含数字 → 不掩
	letters := strings.Repeat("abcdefghij", 4) // 40 letters
	if got := MaskCredentials("word " + letters); strings.Contains(got, "[REDACTED") {
		t.Errorf("letters-only should not mask: %s", got)
	}
}

func TestShannonEntropy(t *testing.T) {
	if shannonEntropy("") != 0 {
		t.Fatal("empty entropy")
	}
	// 均匀 4 字符 → 2 bit
	if h := shannonEntropy("abcd"); h < 1.9 || h > 2.1 {
		t.Errorf("abcd entropy=%v want ~2", h)
	}
	// 重复字符 → 0
	if h := shannonEntropy("aaaa"); h > 0.01 {
		t.Errorf("aaaa entropy=%v want 0", h)
	}
}
