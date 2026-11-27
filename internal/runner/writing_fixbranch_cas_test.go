// writing_fixbranch_cas_test.go —— #62 修复分支连续更新（CAS）：真实裸仓集成。
// 修复分支是持久化版本化产物：下一轮在最新 head 上重建、以归属校验+CAS 更新，
// 不覆盖人工提交、并发只许一个赢家——禁止无条件 --force（roadmap「明确不做」）。
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oekit/logx"
	"github.com/yi-nology/go-git-platform/model"
	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/argusctx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
)

// initCASFixture 裸仓 + 工作副本（feature/a 已推送），返回 work/bare。
func initCASFixture(t *testing.T) (work, bare string) {
	t.Helper()
	base := t.TempDir()
	bare = filepath.Join(base, "bare.git")
	work = filepath.Join(base, "work")
	gitExec(t, base, "init", "-q", "--bare", bare)
	gitExec(t, base, "clone", "-q", bare, work)
	gitExec(t, work, "checkout", "-qb", "feature/a")
	if err := os.WriteFile(filepath.Join(work, "a.go"), []byte("line-one\nline-two\nline-three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, work, "add", "a.go")
	gitExec(t, work, "commit", "-qm", "init")
	gitExec(t, work, "push", "-q", "-u", "origin", "feature/a")
	// 与生产工作副本同形：按 PR SHA detached checkout——短目标名 push 在
	// detached HEAD 下被 git 拒（v8.8.5 实弹），全限定 refs/heads/* 才通过。
	gitExec(t, work, "checkout", "-q", "--detach", "HEAD")
	return work, bare
}

func casRunner(st *storage.Store) *Runner {
	return &Runner{
		Cfg:   &config.Config{FixPush: true, FixPushMode: "branch", FixVerifyCmd: "true"},
		Store: st, Log: logx.Nop(),
		PushEffects: effect.NewFromStore(st, effect.PushTTL, logx.Nop(), true),
	}
}

func casFix(line int, fix string) []domain.Finding {
	return []domain.Finding{
		{File: "a.go", Line: line, Verified: true, SuggestedFix: fix},
	}
}

func workCommitCount(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// 首轮：目标分支不存在 → 普通快进 push（计划中不带 force-with-lease）。
func TestFixBranchFirstRoundPlainPush(t *testing.T) {
	st := gateStore(t)
	work, bare := initCASFixture(t)
	r := casRunner(st)

	plan, err := planFixBranchPush(r, context.Background(), &storage.Task{ID: "t-cas-r1"}, work, "argus/fix/42", nil)
	if err != nil {
		t.Fatalf("首轮计划应成功: %v", err)
	}
	if plan.cas {
		t.Fatal("分支不存在时不得走 CAS")
	}
	want := []string{"-C", work, "push", "origin", "HEAD:refs/heads/argus/fix/42"}
	if strings.Join(plan.argv, " ") != strings.Join(want, " ") {
		t.Fatalf("首轮 push argv = %v, want %v", plan.argv, want)
	}
	// 前置确认：裸仓确无修复分支（rev-parse -q 不存在的 ref 输出空）
	out, _ := exec.Command("git", "-C", bare, "rev-parse", "--verify", "-q", "refs/heads/argus/fix/42").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("裸仓不应已有修复分支: %s", out)
	}
}

// 两轮连续审查：head 前进后第二轮安全更新既有修复分支（非快进场景），不新建。
func TestFixBranchTwoRoundsSafeUpdate(t *testing.T) {
	st := gateStore(t)
	work, bare := initCASFixture(t)
	r := casRunner(st)
	pr := gatePR("42", "feature/a")

	// 第一轮：F1 基于 C1
	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-cas-1"}, pr, work, casFix(1, "fixed-one"), 1)
	f1 := bareRef(t, bare, "refs/heads/argus/fix/42")
	if f1 == "" || f1 == bareRef(t, bare, "refs/heads/feature/a") {
		t.Fatalf("第一轮应建立修复分支: %s", f1)
	}

	// 原 PR head 前进（C2）：先把工作副本脱离 F1 回到 C1（新一轮审查从最新
	// PR head 重建，不含上一轮修复提交），再前进 head——F2 对 F1 构成非快进
	gitExec(t, work, "reset", "-q", "--hard", "origin/feature/a")
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, work, "add", "b.txt")
	gitExec(t, work, "commit", "-qm", "head moves on")
	gitExec(t, work, "push", "-q", "origin", "feature/a")

	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-cas-2"}, pr, work, casFix(2, "fixed-two"), 1)

	f2 := bareRef(t, bare, "refs/heads/argus/fix/42")
	workHead := bareRef(t, work, "HEAD")
	if f2 == f1 {
		t.Fatalf("第二轮应更新修复分支: %s 未变", f2)
	}
	if f2 != workHead {
		t.Fatalf("第二轮后修复分支应指向本轮修复提交: bare=%s work=%s", f2, workHead)
	}
	// 单一修复分支，无第二分支残留
	out, err := exec.Command("git", "-C", bare, "for-each-ref", "--format=%(refname)", "refs/heads/argus/*").Output()
	if err != nil || strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Fatalf("修复分支应唯一: %q err=%v", out, err)
	}
}

// 人工向修复分支 push：下一轮拒绝覆盖（tip 非 argus-bot 提交），落可诊断状态。
func TestFixBranchHumanCommitRefused(t *testing.T) {
	st := gateStore(t)
	work, bare := initCASFixture(t)
	r := casRunner(st)
	pr := gatePR("42", "feature/a")

	// 第一轮建立 F1
	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-human-1"}, pr, work, casFix(1, "fixed-one"), 1)
	f1 := bareRef(t, bare, "refs/heads/argus/fix/42")

	// 人工 clone 推一个非 argus 提交到修复分支
	human := filepath.Join(t.TempDir(), "human")
	gitExec(t, t.TempDir(), "clone", "-q", bare, human)
	gitExec(t, human, "checkout", "-qb", "argus/fix/42", "origin/argus/fix/42")
	if err := os.WriteFile(filepath.Join(human, "human.txt"), []byte("manual\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, human, "add", "human.txt")
	gitExec(t, human, "-c", "user.name=human", "-c", "user.email=human@corp.local", "commit", "-qm", "manual touch")
	gitExec(t, human, "push", "-q", "origin", "argus/fix/42")
	humanHead := bareRef(t, bare, "refs/heads/argus/fix/42")
	if humanHead == f1 {
		t.Fatal("前置失败：人工提交未推上修复分支")
	}

	// 第二轮：拒绝覆盖人工提交
	task2 := &storage.Task{ID: "t-human-2"}
	before := workCommitCount(t, work)
	r.pushVerifiedFixes(context.Background(), task2, pr, work, casFix(2, "fixed-two"), 1)

	if task2.FixStatus != "push_failed" {
		t.Fatalf("人工提交在 tip 应拒: got %q", task2.FixStatus)
	}
	if after := bareRef(t, bare, "refs/heads/argus/fix/42"); after != humanHead {
		t.Fatalf("人工提交被覆盖: %s → %s", humanHead, after)
	}
	if got := workCommitCount(t, work); got != before {
		t.Fatalf("拒推不得产生新提交: %s → %s", before, got)
	}
}

// 并发单胜：CAS 语义下过期 expected 被裸仓拒绝（两个执行者只许一个成功）。
func TestFixBranchStaleLeaseRejected(t *testing.T) {
	st := gateStore(t)
	work, bare := initCASFixture(t)
	r := casRunner(st)
	pr := gatePR("42", "feature/a")

	// 执行者 A 第一轮推送 F1
	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-cas-a"}, pr, work, casFix(1, "fixed-one"), 1)
	f1 := bareRef(t, bare, "refs/heads/argus/fix/42")

	// 执行者 B 在独立 head 上完成第二轮（CAS expected=F1 成功）→ 远端推进到 F2
	gitExec(t, work, "reset", "-q", "--hard", "origin/feature/a")
	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-cas-b"}, pr, work, casFix(2, "fixed-two"), 1)
	f2 := bareRef(t, bare, "refs/heads/argus/fix/42")
	if f2 == f1 {
		t.Fatal("前置失败：B 轮应推进修复分支")
	}

	// 执行者 A 此后才推自己持过期 expected=F1 的计划——裸仓必须拒绝
	if err := os.WriteFile(filepath.Join(work, "c.txt"), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, work, "add", "c.txt")
	gitExec(t, work, "-c", "user.name=argus-bot", "-c", "user.email=argus@bot.local",
		"commit", "-qm", "fix(argus): stale contender")
	stale := []string{"-C", work, "push",
		"--force-with-lease=refs/heads/argus/fix/42:" + f1, "origin",
		"HEAD:refs/heads/argus/fix/42"}
	if _, stderr, ok := r.gitRun(context.Background(), work, stale); ok {
		t.Fatalf("过期 lease 的 CAS push 必须被拒绝: %s", stderr)
	}
	if after := bareRef(t, bare, "refs/heads/argus/fix/42"); after != f2 {
		t.Fatalf("远端被过期计划改写: %s → %s", f2, after)
	}
}

// 两轮连续审查（真实裸仓 + 平台打桩）：第二轮 CAS 更新后复用同一修复 PR，
// 不新建第二个——#62 验收「第二轮安全更新远程修复分支并复用同一 PR」的
// 端到端形态（分支 CAS 与 PR 查重此前分属两个测试）。
func TestFixBranchTwoRoundsReuseSingleFixPR(t *testing.T) {
	st := gateStore(t)
	work, _ := initCASFixture(t)
	plat := &fixPRFake{}
	r := casRunner(st)
	r.SDK = argusctx.NewSDKCtxStubbed(logx.Nop(), map[string]provider.Provider{"github": plat})
	pr := gatePR("42", "feature/a")

	// 第一轮：开修复 PR + 通知原 PR
	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-pr-r1"}, pr, work, casFix(1, "fixed-one"), 1)
	opts, notes := plat.snapshot()
	if len(opts) != 1 || len(notes) != 1 {
		t.Fatalf("第一轮应开一个修复 PR 并通知: opts=%d notes=%d", len(opts), len(notes))
	}
	// 平台侧已有第一轮创建的 open 修复 PR（查重收敛输入）
	plat.mu.Lock()
	plat.existing = []*model.ChangeRequest{{
		Number: "77", State: model.CRStateOpened,
		SourceBranch: opts[0].SourceBranch, TargetBranch: opts[0].TargetBranch,
		WebURL: "https://example.com/o/r/pull/77",
	}}
	plat.mu.Unlock()

	// 原 PR head 前进：第二轮从最新 head 重建，对既有修复分支构成非快进
	gitExec(t, work, "reset", "-q", "--hard", "origin/feature/a")
	if err := os.WriteFile(filepath.Join(work, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, work, "add", "b.txt")
	gitExec(t, work, "commit", "-qm", "head moves on")
	gitExec(t, work, "push", "-q", "origin", "feature/a")

	r.pushVerifiedFixes(context.Background(), &storage.Task{ID: "t-pr-r2"}, pr, work, casFix(2, "fixed-two"), 1)

	opts, notes = plat.snapshot()
	if len(opts) != 1 {
		t.Fatalf("第二轮不得新建修复 PR（应复用 open PR）: CreateCR=%d", len(opts))
	}
	if len(notes) != 2 || !strings.Contains(notes[1], "修复已更新至") {
		t.Fatalf("第二轮应回帖更新既有修复 PR: notes=%v", notes)
	}
	// 修复分支仍唯一（CAS 更新，非新开分支）
	out, err := exec.Command("git", "-C", work, "ls-remote", "--heads", "origin", "argus/*").Output()
	if err != nil || strings.Count(string(out), "refs/heads/argus/fix/42") != 1 {
		t.Fatalf("修复分支应唯一 argus/fix/42: %q err=%v", out, err)
	}
}
