package runner

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
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

// TestAuthGitEnv 写凭证注入形态（#66：argv 暴露面清零）：凭证经
// GIT_CONFIG_COUNT/KEY_0/VALUE_0 环境变量注入（git 官方 env-config 协议），
// 不再出现在子进程 argv（ps 可见面）；WriteToken 优先、缺省回落 Token、
// 全缺返回 nil（依赖环境凭证兜底）。
func TestAuthGitEnv(t *testing.T) {
	r := &Runner{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"github": {Token: "read-tok"},
		"gitlab": {Token: "read-tok", WriteToken: "write-tok"},
		"gitea":  {}, // 无凭证
	}}}

	// WriteToken 优先；env 三元组形态 + 无 Authorization 泄漏面断言
	env := r.authGitEnv("gitlab")
	if len(env) != 3 || env[0] != "GIT_CONFIG_COUNT=1" ||
		env[1] != "GIT_CONFIG_KEY_0=http.extraheader" {
		t.Fatalf("gitlab env 三元组形态错: %v", env)
	}
	if !strings.HasPrefix(env[2], "GIT_CONFIG_VALUE_0=Authorization: Basic ") {
		t.Fatalf("VALUE_0 应为 Basic 头: %q", env[2])
	}
	// 解码验证 oauth2:write-tok
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(env[2],
		"GIT_CONFIG_VALUE_0=Authorization: Basic "))
	if err != nil || string(raw) != "oauth2:write-tok" {
		t.Fatalf("Basic 载荷 = %q err=%v, want oauth2:write-tok", raw, err)
	}

	// 回落 Token
	if env := r.authGitEnv("github"); len(env) != 3 {
		t.Fatalf("github 应回落 Token 注入, got %v", env)
	}

	// 全缺 → nil（环境凭证兜底）
	if env := r.authGitEnv("gitea"); env != nil {
		t.Fatalf("无凭证应 nil, got %v", env)
	}
	// 未配置平台 → nil
	if env := r.authGitEnv("nope"); env != nil {
		t.Fatalf("未知平台应 nil, got %v", env)
	}
}

// TestRunFixGitPushStepClassification 失败步骤显式分类（#66：不再 argv[2] 猜）：
// 有写凭证时 commit 失败 → commit_failed；push 失败 → push_failed——附带凭证的
// argv 下标法会把两者都误判成 "-c_failed"，本测试钉住 gitStep 显式 step 标签。
func TestRunFixGitPushStepClassification(t *testing.T) {
	st := gateStore(t)
	// 有写凭证的平台：旧 argv 形态下 commit/push 的 argv[2] 都是 "-c"
	r := &Runner{
		Cfg: &config.Config{FixPush: true, FixPushMode: "branch", Platforms: map[string]config.PlatformConfig{
			"github": {Token: "read-tok", WriteToken: "write-tok"},
		}},
		Store: st, Log: logx.Nop(),
		PushEffects: effect.NewFromStore(st, effect.PushTTL, logx.Nop(), true),
	}
	authEnv := r.authGitEnv("github")
	if authEnv == nil {
		t.Fatal("前置失败：写凭证平台应产出 authGitEnv")
	}

	// commit 失败：nothing to commit（树无变更，commit 退出码 1）
	dir := initPushRepo(t)
	task := &storage.Task{ID: "t-step-commit"}
	steps := []gitStep{
		{step: "add", argv: []string{"-C", dir, "add", "--", "a.go"}},
		{step: "commit", argv: []string{"-C", dir, "-c", "user.name=argus-bot",
			"-c", "user.email=argus@bot.local", "commit", "-m", "fix"}, env: authEnv},
	}
	err := runFixGitPush(r, context.Background(), task, dir, steps, "branch")
	if err == nil || !strings.Contains(err.Error(), "commit_failed") {
		t.Fatalf("commit 失败应报 commit_failed, got %v", err)
	}
	if task.FixStatus != "push_failed" {
		t.Fatalf("FixStatus 应 push_failed, got %q", task.FixStatus)
	}

	// push 失败：远端不存在（树上有真实变更，add/commit 均成功）
	task2 := &storage.Task{ID: "t-step-push"}
	dir2 := initPushRepo(t)
	if err := os.WriteFile(filepath.Join(dir2, "a.go"), []byte("line-one\nline-two\nfixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir2, "remote", "add", "origin", filepath.Join(dir2, "no-such-remote.git"))
	pushSteps := []gitStep{
		{step: "add", argv: []string{"-C", dir2, "add", "--", "a.go"}},
		{step: "commit", argv: []string{"-C", dir2, "-c", "user.name=argus-bot",
			"-c", "user.email=argus@bot.local", "commit", "-qm", "fix"}, env: authEnv},
		{step: "push", argv: []string{"-C", dir2, "push", "origin",
			"HEAD:argus/fix/42"}, env: authEnv},
	}
	err = runFixGitPush(r, context.Background(), task2, dir2, pushSteps, "branch")
	if err == nil || !strings.Contains(err.Error(), "push_failed") {
		t.Fatalf("push 失败应报 push_failed, got %v", err)
	}
	if task2.FixStatus != "push_failed" {
		t.Fatalf("FixStatus 应 push_failed, got %q", task2.FixStatus)
	}
}

// TestGitEnvCredentialReachesGit 凭证 env 端到端（#66）：authGitEnv 产物经
// gitRunEnv 传入真实 git——env-config 协议生效（git config --get 能读到
// http.extraheader），且全程不经过 argv。
func TestGitEnvCredentialReachesGit(t *testing.T) {
	r := &Runner{Cfg: &config.Config{Platforms: map[string]config.PlatformConfig{
		"github": {Token: "read-tok", WriteToken: "write-tok"},
	}}}
	dir := initPushRepo(t)
	env := r.authGitEnv("github")
	_, stderr, ok := gitRunEnv(r, context.Background(), dir,
		[]string{"-C", dir, "config", "--get", "http.extraheader"}, env)
	if !ok {
		t.Fatalf("env-config 未生效（git 读不到 http.extraheader）: %s", stderr)
	}
	if strings.Contains(stderr, "write-tok") {
		t.Fatalf("stderr 不应回显 token: %s", stderr)
	}
}
