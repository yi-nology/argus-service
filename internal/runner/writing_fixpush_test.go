package runner

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
)

// TestFixBranchName 修复分支命名（Phase 3 按 PR 收敛）：argus/fix/<prNumber>
// 自有前缀——与常见保护分支（main/master/release/*）无交集，branch 形态
// 任何推送目标都不可能落在保护分支上。
func TestFixBranchName(t *testing.T) {
	got := fixBranchName(&plugin.PRContext{Number: "42"})
	want := "argus/fix/42"
	if got != want {
		t.Fatalf("fixBranchName = %q, want %q", got, want)
	}
	// 修复分支永不与保护分支词表相等（前缀保证）
	for _, protected := range []string{"main", "master", "release", "develop"} {
		if got == protected {
			t.Errorf("修复分支名 %q 撞保护分支 %q", got, protected)
		}
	}
}

// TestIsFixBranch 防循环护栏（v7.0 阶段 2）：源分支属 argus/fix/* 命名空间
// = 本环自产修复 PR，push 环必须跳过（否则无限开修复 PR）；普通分支不受影响。
// fixBranchName 产物必须被护栏识别（两函数共用 fixBranchPrefix 单源的回归钉）。
func TestIsFixBranch(t *testing.T) {
	cases := []struct {
		branch string
		want   bool
	}{
		{"argus/fix/t-abc123def456789011131500", true}, // 本环产物（fixBranchName 形态）
		{"argus/fix/anything", true},                   // 命名空间即拦，不校验 task 形态
		{"feature/foo", false},                         // 普通特性分支
		{"argus/fix", false},                           // 前缀缺斜杠不误伤
		{"argus/fixtures/x", false},                    // 相似前缀不误伤（fix ≠ fixtures）
		{"main", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isFixBranch(c.branch); got != c.want {
			t.Errorf("isFixBranch(%q) = %v, want %v", c.branch, got, c.want)
		}
	}
	// 单源钉：fixBranchName 产物恒被护栏识别
	if !isFixBranch(fixBranchName(&plugin.PRContext{Number: "7"})) {
		t.Error("fixBranchName 产物未被 isFixBranch 识别——两函数前缀漂移")
	}
}

// TestAuthGitArgs 写凭证注入形态（Phase 3，同 workcopy tools#124）：
// `-c http.extraheader=Authorization: Basic base64(oauth2:token)` 进程级形态；
// WriteToken 优先、缺省回落 Token、全缺返回 nil（依赖环境凭证兜底）。
func TestAuthGitArgs(t *testing.T) {
	r := &Runner{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"github": {Token: "read-tok"},
		"gitlab": {Token: "read-tok", WriteToken: "write-tok"},
		"gitea":  {}, // 无凭证
	}}}

	// WriteToken 优先
	args := r.authGitArgs("gitlab")
	if len(args) != 2 || args[0] != "-c" ||
		!strings.HasPrefix(args[1], "http.extraheader=Authorization: Basic ") {
		t.Fatalf("gitlab extraheader 形态错: %v", args)
	}
	// 解码验证 oauth2:write-tok
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(args[1],
		"http.extraheader=Authorization: Basic "))
	if err != nil || string(raw) != "oauth2:write-tok" {
		t.Fatalf("Basic 载荷 = %q err=%v, want oauth2:write-tok", raw, err)
	}

	// 回落 Token
	if args := r.authGitArgs("github"); len(args) != 2 {
		t.Fatalf("github 应回落 Token 注入, got %v", args)
	}

	// 全缺 → nil（环境凭证兜底）
	if args := r.authGitArgs("gitea"); args != nil {
		t.Fatalf("无凭证应 nil, got %v", args)
	}
	// 未配置平台 → nil
	if args := r.authGitArgs("nope"); args != nil {
		t.Fatalf("未知平台应 nil, got %v", args)
	}
}
