// side_effect_resolver_adapters_test.go —— #64 对账探针的宿主面回归：
//   - git ls-remote 分支探测实弹（本地 file:// 裸仓库，URL 派生全链路）；
//   - 平台提交检索/PR 检索的打桩判据（任务标记/源分支过滤）；
//   - 探针 → resolver → Reconciler 的端到端收敛（分支存在=done / 缺席=签字放行）。
package bootstrap

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"
	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
)

// seedBareRepoWithBranch 造本地裸仓库（file:// 平台形态）并推一个分支，
// 返回（BaseURL, 修复分支 tip SHA）。目录布局对齐 CloneURL 派生：
// <root>/myorg/app.git 即远端仓库。
func seedBareRepoWithBranch(t *testing.T, branch string) (baseURL, tip string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "myorg", "app.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=argus-bot", "GIT_AUTHOR_EMAIL=argus@bot.local",
			"GIT_COMMITTER_NAME=argus-bot", "GIT_COMMITTER_EMAIL=argus@bot.local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git(bare, "init", "--bare", "-b", "main")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git(work, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(work, "add", ".")
	git(work, "commit", "-m", "fix(argus): seed\n\nTask: t-1\n")
	git(work, "remote", "add", "origin", bare)
	git(work, "push", "-q", "origin", "main")
	if branch != "" {
		git(work, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	}
	out := git(work, "rev-parse", "HEAD")
	return "file://" + root, firstLine(out)
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// probeFixture 装配探针（cfg.BaseURL → file:// 裸库；SDK 打桩空表）。
func probeFixture(t *testing.T, baseURL string) effectPushProbe {
	t.Helper()
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{
		"gitea": {BaseURL: baseURL},
	}}
	sdk := argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{"gitea": &probeProviderStub{}})
	return effectPushProbe{cfg: cfg, sdk: sdk}
}

func TestPushProbeBranchTipSHA(t *testing.T) {
	baseURL, tip := seedBareRepoWithBranch(t, "argus/fix/7")
	probe := probeFixture(t, baseURL)
	ctx := context.Background()

	// 分支存在：tip SHA 命中（URL 派生 → ls-remote 全链路实弹）。
	sha, ok, err := probe.BranchTipSHA(ctx, "gitea", "myorg", "app", "argus/fix/7")
	if err != nil || !ok {
		t.Fatalf("BranchTipSHA = (%q,%v,%v)", sha, ok, err)
	}
	if sha != tip {
		t.Fatalf("tip = %q, want %q", sha, tip)
	}
	// 分支不存在：ok=false 且无错（显式「未生效」证据的形态）。
	if sha, ok, err = probe.BranchTipSHA(ctx, "gitea", "myorg", "app", "argus/fix/8"); err != nil || ok || sha != "" {
		t.Fatalf("absent = (%q,%v,%v), want (\"\",false,nil)", sha, ok, err)
	}
	// 平台未配置 BaseURL：fail-closed 判据不足。
	if _, ok, err = probe.TaskCommitOnBranch(ctx, "unknown", "myorg", "app", "main", "t-1"); err == nil || ok {
		t.Fatalf("未配置平台应报判据不足: ok=%v err=%v", ok, err)
	}
}

// probeProviderStub provider.Provider 的最小打桩（仅 ListCommits/ListCRs）。
type probeProviderStub struct {
	provider.Provider
	commits []*model.CommitInfo
	listErr error
	crs     []*model.ChangeRequest
}

func (s *probeProviderStub) ListCommits(_ context.Context, _, _ string, _ provider.ListCommitsOptions) ([]*model.CommitInfo, error) {
	return s.commits, s.listErr
}

func (s *probeProviderStub) ListCRs(_ context.Context, _ provider.ListCROptions) ([]*model.ChangeRequest, *int, error) {
	return s.crs, nil, s.listErr
}

func TestPushProbeTaskCommitOnBranch(t *testing.T) {
	baseURL, _ := seedBareRepoWithBranch(t, "")
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{"gitea": {BaseURL: baseURL}}}
	// 近期提交含任务标记（作者以 subject 前缀 + Task: 行判定，CRUser 无邮箱）。
	commits := []*model.CommitInfo{
		{SHA: "human1", Message: "feat: 人工提交\n", Author: &model.CRUser{Username: "dev"}},
		{SHA: "bot1", Message: "fix(argus): 修复\n\nTask: t-9\nPR: myorg/app#7\n",
			Author: &model.CRUser{Username: "argus-bot"}},
	}
	sdk := argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{
		"gitea": &probeProviderStub{commits: commits},
	})
	probe := effectPushProbe{cfg: cfg, sdk: sdk}

	sha, ok, err := probe.TaskCommitOnBranch(context.Background(), "gitea", "myorg", "app", "main", "t-9")
	if err != nil || !ok || sha != "bot1" {
		t.Fatalf("TaskCommitOnBranch = (%q,%v,%v), want bot1", sha, ok, err)
	}
	// 任务标记不匹配 → 未命中。
	if _, ok, err = probe.TaskCommitOnBranch(context.Background(), "gitea", "myorg", "app", "main", "t-other"); err != nil || ok {
		t.Fatalf("标记不匹配应未命中: ok=%v err=%v", ok, err)
	}
	// 平台检索失败 = 判据不足。
	bad := effectPushProbe{cfg: cfg, sdk: argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{
		"gitea": &probeProviderStub{listErr: context.DeadlineExceeded},
	})}
	if _, ok, err = bad.TaskCommitOnBranch(context.Background(), "gitea", "myorg", "app", "main", "t-9"); err == nil || ok {
		t.Fatalf("检索失败应透传: ok=%v err=%v", ok, err)
	}
}

func TestSDKCRFinder(t *testing.T) {
	sdk := argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{
		"gitea": &probeProviderStub{crs: []*model.ChangeRequest{
			{ID: 42, Number: "13", WebURL: "https://git/myorg/app/-/merge_requests/13",
				SourceBranch: "argus/fix/7", TargetBranch: "main"},
			{ID: 43, Number: "14", SourceBranch: "feature-x"},
		}},
	})
	f := sdkCRFinder{sdk: sdk}
	id, number, url, ok, err := f.FindCRBySource(context.Background(), "gitea", "myorg", "app", "argus/fix/7")
	if err != nil || !ok || id != 42 || number != "13" || url == "" {
		t.Fatalf("FindCRBySource = (%d,%q,%q,%v,%v)", id, number, url, ok, err)
	}
	// 无命中 → ok=false（显式未生效证据）。
	if _, _, _, ok, err = f.FindCRBySource(context.Background(), "gitea", "myorg", "app", "argus/fix/8"); err != nil || ok {
		t.Fatalf("无命中 = ok=%v err=%v, want false,nil", ok, err)
	}
	// 平台查询失败 = 判据不足。
	bad := sdkCRFinder{sdk: argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{
		"gitea": &probeProviderStub{listErr: context.Canceled},
	})}
	if _, _, _, _, err = bad.FindCRBySource(context.Background(), "gitea", "myorg", "app", "argus/fix/7"); err == nil {
		t.Fatal("查询失败应透传 error")
	}
}

// TestReconcilerResolvesPushUncertain 端到端：uncertain push Key → 探针查裸库 →
// 分支存在=done（tip 落载荷）；分支缺席=签字放行回 pending。
func TestReconcilerResolvesPushUncertain(t *testing.T) {
	baseURL, _ := seedBareRepoWithBranch(t, "argus/fix/7")
	probe := probeFixture(t, baseURL)
	// Reconciler 走 fresh admin store（真实时钟）：种子行 updated_at 回拨 1h
	//（远超 grace 窗）——种子写入用注入时钟。
	seedClock := time.Now().UTC().Add(-time.Hour)

	dsn := "file://" + t.TempDir() + "/recon.db"
	store, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedUncertainPush(t, store, seedClock, "fix:push:t-1:abc:branch",
		"branch:gitea:myorg/app:argus/fix/7")

	// 分支存在 → Applied → done + tip 载荷。
	resolver := effect.NewRouterResolver(effect.NewPushResolver(probe))
	rc := effect.NewReconciler(store, resolver, nil)
	resolved, skipped, err := rc.ReconcileOnce(context.Background())
	if err != nil || resolved != 1 || skipped != 0 {
		t.Fatalf("ReconcileOnce = (%d,%d,%v), want (1,0,nil)", resolved, skipped, err)
	}
	acts, _ := store.SideEffectAdmin().ListUncertain(context.Background(), time.Now().Add(time.Hour), 10)
	if len(acts) != 0 {
		t.Fatalf("uncertain 残留: %+v", acts)
	}

	// 分支缺席 → 签字放行回 pending（可重试，等真重试或人工）。
	seedUncertainPush(t, store, seedClock, "fix:push:t-2:def:branch",
		"branch:gitea:myorg/app:argus/fix/8")
	resolved, skipped, err = rc.ReconcileOnce(context.Background())
	if err != nil || resolved != 1 || skipped != 0 {
		t.Fatalf("absent ReconcileOnce = (%d,%d,%v), want (1,0,nil)", resolved, skipped, err)
	}
	acts, _ = store.SideEffectAdmin().ListUncertain(context.Background(), time.Now().Add(time.Hour), 10)
	if len(acts) != 0 {
		t.Fatalf("absent 分支裁决后 uncertain 残留: %+v", acts)
	}
}

// TestReconcilerResolvesCRUncertain 端到端：修复 PR Key → SDK 打桩命中 → done。
func TestReconcilerResolvesCRUncertain(t *testing.T) {
	cfg := &config.Config{Platforms: map[string]config.PlatformConfig{"gitea": {BaseURL: "file:///nonexistent"}}}
	sdk := argusctx.NewSDKCtxStubbed(nil, map[string]provider.Provider{
		"gitea": &probeProviderStub{crs: []*model.ChangeRequest{
			{ID: 42, Number: "13", SourceBranch: "argus/fix/7"},
		}},
	})
	store, err := storage.Open("file://" + t.TempDir() + "/recon-cr.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedUncertainPush(t, store, time.Now().UTC().Add(-time.Hour), "fix:cr:t-1:abc:branch", "pr:gitea:myorg/app#7")

	rc := effect.NewReconciler(store, effect.NewRouterResolver(
		effect.NewCRResolver(sdkCRFinder{sdk: sdk}), effect.NewPushResolver(effectPushProbe{cfg: cfg, sdk: sdk}),
	), nil)
	resolved, skipped, err := rc.ReconcileOnce(context.Background())
	if err != nil || resolved != 1 || skipped != 0 {
		t.Fatalf("CR ReconcileOnce = (%d,%d,%v), want (1,0,nil)", resolved, skipped, err)
	}
	acts, _ := store.SideEffectAdmin().ListUncertain(context.Background(), time.Now().Add(time.Hour), 10)
	if len(acts) != 0 {
		t.Fatalf("uncertain 残留: %+v", acts)
	}
}

// seedUncertainPush 造一条 uncertain 的 push/CR 动作行（Acquire → MarkUncertain
// → Release），时钟注入使 updated_at 落在 grace 窗之外。
func seedUncertainPush(t *testing.T, store *storage.Store, clock time.Time, key, resource string) {
	t.Helper()
	st := store.SideEffects()
	st.UseClock(func() time.Time { return clock })
	act := sideeffect.Action{Key: sideeffect.Key(key), Resource: resource,
		Payload: []byte("payload-" + key), RetrySafety: sideeffect.RetrySafetyUnsafe}
	ctx := context.Background()
	outcome, lease, _, err := st.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(%s) outcome=%v err=%v", key, outcome, err)
	}
	if err := st.MarkUncertain(ctx, act, lease, "runner 中断"); err != nil {
		t.Fatalf("MarkUncertain: %v", err)
	}
	if err := st.Release(ctx, act, lease); err != nil {
		t.Fatalf("Release: %v", err)
	}
}
