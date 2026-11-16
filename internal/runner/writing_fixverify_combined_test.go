// writing_fixverify_combined_test.go —— #61 组合补丁最终验证的推送环集成测试：
// 逐条通过、组合失败 → 不 commit/push；包装命令越界改 tracked 文件 → 拒绝；
// 组合通过 → 与既有推送行为一致（真实裸仓端到端）。
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/storage"
)

// initPushRepoWithRemote 建带真实远端的工作仓：bare.git + 工作副本（feature/a
// 分支，a.go 三行 + b.go 一行已提交并推送）。
func initPushRepoWithRemote(t *testing.T) (work, bare string) {
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
	if err := os.WriteFile(filepath.Join(work, "b.go"), []byte("stable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, work, "add", "a.go", "b.go")
	gitExec(t, work, "commit", "-qm", "init")
	gitExec(t, work, "push", "-q", "-u", "origin", "feature/a")
	return work, bare
}

func combinedRunner(st *storage.Store, verifyCmd string) *Runner {
	return &Runner{
		Cfg:   &config.Config{FixPush: true, FixPushMode: "direct", FixVerifyCmd: verifyCmd},
		Store: st, Log: logx.Nop(),
		PushEffects: effect.NewFromStore(st, effect.PushTTL, logx.Nop(), true),
	}
}

func combinedFindings() []domain.Finding {
	return []domain.Finding{
		{File: "a.go", Line: 1, Verified: true, SuggestedFix: "fixed-one"},
		{File: "a.go", Line: 2, Verified: true, SuggestedFix: "fixed-two"},
	}
}

func bareRef(t *testing.T, bare, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// 单条各自通过、组合失败（两标记同现才失败）：不得 commit/push，落
// push_failed，工作副本清场还原。
func TestCombinedVerifyFailureBlocksPush(t *testing.T) {
	st := gateStore(t)
	work, bare := initPushRepoWithRemote(t)
	before := bareRef(t, bare, "feature/a")
	// 逐条语义（单标记在场）通过；组合树（两标记同现）失败——正是 #61 的雷
	r := combinedRunner(st, `if grep -q fixed-one {dir}/a.go && grep -q fixed-two {dir}/a.go; then echo combo-conflict >&2; exit 1; fi`)
	task := &storage.Task{ID: "t-combo-fail"}
	pr := gatePR("42", "feature/a")

	r.pushVerifiedFixes(context.Background(), task, pr, work, combinedFindings(), 2)

	if task.FixStatus != "push_failed" {
		t.Fatalf("组合失败应落 push_failed, got %q", task.FixStatus)
	}
	if out, err := exec.Command("git", "-C", work, "rev-list", "--count", "HEAD").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("组合失败不得产生新提交: out=%q err=%v", out, err)
	}
	if after := bareRef(t, bare, "feature/a"); after != before {
		t.Fatalf("组合失败不得推送: bare %s → %s", before, after)
	}
	// 清场还原：Apply 写入的两处修复被 reset --hard 撤销
	raw, err := os.ReadFile(filepath.Join(work, "a.go"))
	if err != nil || !strings.HasPrefix(string(raw), "line-one") {
		t.Fatalf("组合失败后工作副本应清场还原: %q err=%v", raw, err)
	}
}

// 包装命令通过但越界修改 tracked 文件（b.go 不在预期写入集）：拒绝推送。
func TestDiffScopeViolationBlocksPush(t *testing.T) {
	st := gateStore(t)
	work, bare := initPushRepoWithRemote(t)
	before := bareRef(t, bare, "feature/a")
	// 包装命令 exit 0（组合验证通过）但顺带改了预期外的 tracked 文件
	r := combinedRunner(st, `echo dirty >> {dir}/b.go`)
	task := &storage.Task{ID: "t-scope-violation"}
	pr := gatePR("42", "feature/a")

	r.pushVerifiedFixes(context.Background(), task, pr, work, combinedFindings(), 2)

	if task.FixStatus != "push_failed" {
		t.Fatalf("diff 越界应落 push_failed, got %q", task.FixStatus)
	}
	if out, err := exec.Command("git", "-C", work, "rev-list", "--count", "HEAD").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("diff 越界不得产生新提交: out=%q err=%v", out, err)
	}
	if after := bareRef(t, bare, "feature/a"); after != before {
		t.Fatalf("diff 越界不得推送: bare %s → %s", before, after)
	}
}

// 组合通过：行为与既有推送一致——新提交产生并推到远端，状态 pushed（direct）。
func TestCombinedPassPushesEndToEnd(t *testing.T) {
	st := gateStore(t)
	work, bare := initPushRepoWithRemote(t)
	r := combinedRunner(st, `test -f {dir}/a.go`)
	task := &storage.Task{ID: "t-combo-pass"}
	pr := gatePR("42", "feature/a")

	r.pushVerifiedFixes(context.Background(), task, pr, work, combinedFindings(), 2)

	if task.FixStatus != "pushed" {
		t.Fatalf("组合通过应推送成功 pushed, got %q", task.FixStatus)
	}
	out, err := exec.Command("git", "-C", work, "rev-list", "--count", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != "2" {
		t.Fatalf("组合通过应产生修复提交: out=%q err=%v", out, err)
	}
	workHead := bareRef(t, work, "HEAD")
	if after := bareRef(t, bare, "feature/a"); after != workHead {
		t.Fatalf("远端应推进到工作副本 HEAD: bare=%s work=%s", after, workHead)
	}
	// 组合验证通过后才允许的措辞：提交信息带组合验证声明
	msg, err := exec.Command("git", "-C", work, "log", "-1", "--format=%s%n%b").Output()
	if err != nil || !strings.Contains(string(msg), "组合") {
		t.Fatalf("提交信息应含组合验证声明: %q err=%v", msg, err)
	}
}

// 包装命令把越界改动 stage 进索引（普通 `git diff` 只看未暂存改动）：diff
// 范围校验必须覆盖 index，否则随后的 commit 会带上预期外 tracked 文件。
func TestStagedOutOfScopeChangeBlocksPush(t *testing.T) {
	st := gateStore(t)
	work, bare := initPushRepoWithRemote(t)
	before := bareRef(t, bare, "feature/a")
	// exit 0（组合验证通过）但把 b.go 的越界改动 stage 进索引
	r := combinedRunner(st, `echo dirty >> {dir}/b.go && git -C {dir} add b.go`)
	task := &storage.Task{ID: "t-staged-scope"}
	pr := gatePR("42", "feature/a")

	r.pushVerifiedFixes(context.Background(), task, pr, work, combinedFindings(), 2)

	if task.FixStatus != "push_failed" {
		t.Fatalf("暂存越界改动应落 push_failed, got %q", task.FixStatus)
	}
	if out, err := exec.Command("git", "-C", work, "rev-list", "--count", "HEAD").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("暂存越界不得产生新提交: out=%q err=%v", out, err)
	}
	if after := bareRef(t, bare, "feature/a"); after != before {
		t.Fatalf("暂存越界不得推送: bare %s → %s", before, after)
	}
}

// 包装命令在验证期自行 commit（HEAD 前移）：越界改动被藏进新提交，普通
// diff 不可见——必须检测 HEAD 移动并拒绝。
func TestWrapperCommitBlocksPush(t *testing.T) {
	st := gateStore(t)
	work, bare := initPushRepoWithRemote(t)
	before := bareRef(t, bare, "feature/a")
	// 组合验证 exit 0，但把越界改动提交掉（HEAD 前移）
	r := combinedRunner(st, `echo dirty >> {dir}/b.go && git -C {dir} add b.go && `+
		`git -C {dir} -c user.name=argus-bot -c user.email=argus@bot.local commit -qm "fix(argus): wrapper commit"`)
	task := &storage.Task{ID: "t-wrapper-commit"}
	pr := gatePR("42", "feature/a")

	r.pushVerifiedFixes(context.Background(), task, pr, work, combinedFindings(), 2)

	if task.FixStatus != "push_failed" {
		t.Fatalf("包装命令移动 HEAD 应落 push_failed, got %q", task.FixStatus)
	}
	if out, err := exec.Command("git", "-C", work, "rev-list", "--count", "HEAD").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("不得保留包装命令的提交: out=%q err=%v", out, err)
	}
	if after := bareRef(t, bare, "feature/a"); after != before {
		t.Fatalf("不得推送: bare %s → %s", before, after)
	}
}
