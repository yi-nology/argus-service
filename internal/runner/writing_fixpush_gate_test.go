// writing_fixpush_gate_test.go —— 推送副作用闸的接线回归：
// 资源按目标分支隔离、并发同 Key 只施加一次、被占时落终态 push_skipped。
package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/effect"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

func gitExec(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=argus-test", "GIT_AUTHOR_EMAIL=argus@test.local",
		"GIT_COMMITTER_NAME=argus-test", "GIT_COMMITTER_EMAIL=argus@test.local")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v 失败: %v\n%s", args, err, out)
	}
}

// initPushRepo 建一个带一行已提交文件的临时仓库（推送闸只碰 git 元数据）。
func initPushRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitExec(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("line-one\nline-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitExec(t, dir, "add", "a.go")
	gitExec(t, dir, "commit", "-qm", "init")
	return dir
}

func gateStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func gateRunner(st *storage.Store) *Runner {
	return &Runner{
		Cfg:         &config.Config{FixPush: true, FixPushMode: "direct"},
		Store:       st,
		Log:         logx.Nop(),
		PushEffects: effect.NewFromStore(st, effect.PushTTL, logx.Nop(), true),
	}
}

func gatePR(number, head string) *plugin.PRContext {
	return &plugin.PRContext{
		Platform: "github", Owner: "acme", Repo: "pay-gateway",
		Number: number, HeadBranch: head,
	}
}

// 资源按目标分支/PR 隔离：同仓不同 PR 的推送与开 PR 不再互相占用。
func TestPushEffectActionPerTargetResource(t *testing.T) {
	pr42, pr43 := gatePR("42", "feature/a"), gatePR("43", "feature/b")
	push42 := pushEffectAction("t1", "sha1", "branch", "argus/fix/42", pr42)
	push43 := pushEffectAction("t2", "sha2", "branch", "argus/fix/43", pr43)

	if want := "fix:push:t1:sha1:branch"; string(push42.Key) != want {
		t.Errorf("push key = %q, want %q", push42.Key, want)
	}
	if want := "branch:github:acme/pay-gateway:argus/fix/42"; push42.Resource != want {
		t.Errorf("push resource = %q, want %q", push42.Resource, want)
	}
	if push42.Resource == push43.Resource {
		t.Error("不同 PR 的修复推送不得共享资源")
	}
	if directA, directB := pushEffectAction("t1", "sha1", "direct", "feature/a", pr42),
		pushEffectAction("t2", "sha2", "direct", "feature/b", pr43); directA.Resource == directB.Resource {
		t.Error("不同目标分支的 direct 推送不得共享资源")
	}

	cr42 := crEffectAction("t1", "sha1", "argus/fix/42", pr42)
	cr43 := crEffectAction("t2", "sha2", "argus/fix/43", pr43)
	if want := "fix:cr:t1:sha1:branch"; string(cr42.Key) != want {
		t.Errorf("cr key = %q, want %q", cr42.Key, want)
	}
	if want := "pr:github:acme/pay-gateway#42"; cr42.Resource != want {
		t.Errorf("cr resource = %q, want %q", cr42.Resource, want)
	}
	if cr42.Resource == cr43.Resource {
		t.Error("不同 PR 的开 PR 动作不得共享资源")
	}
}

// 并发同 Key：runFixGitPush 只执行一次，另一个请求被重放/占用挡住。
func TestPushGateConcurrentSameKeyAppliesOnce(t *testing.T) {
	st := gateStore(t)
	r := gateRunner(st)
	dir := initPushRepo(t)
	task := &storage.Task{ID: "t-gate-once"}
	pr := gatePR("42", "feature/a")
	act := pushEffectAction(task.ID, "sha-once", "direct", pr.HeadBranch, pr)
	gitSteps := []gitStep{
		{step: "add", argv: []string{"-C", dir, "add", "--", "a.go"}},
		{step: "commit", argv: []string{"-C", dir, "commit", "-qm", "fix"}},
	}

	var applies atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := r.pushEffectRunner().Do(context.Background(), act,
				func(ctx context.Context, _ effect.Lease) (effect.Result, error) {
					applies.Add(1)
					if err := runFixGitPush(r, ctx, task, dir, gitSteps, "direct"); err != nil {
						return effect.Result{}, err
					}
					return effect.Result{Payload: []byte("ok")}, nil
				})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !effect.RetryBlocked(err) {
			t.Fatalf("只允许可重试的占用错误: %v", err)
		}
	}
	if applies.Load() != 1 {
		t.Fatalf("同一 Key 并发只允许施加一次: %d", applies.Load())
	}
}

// 资源被占（不同 Key 持同一分支资源）：pushVerifiedFixes 不得留 pushing，
// 必须落终态 push_skipped，且不产生新提交。
func TestPushVerifiedFixesContendedSetsSkipped(t *testing.T) {
	st := gateStore(t)
	r := gateRunner(st)
	dir := initPushRepo(t)
	pr := gatePR("42", "feature/a")
	ctx := context.Background()

	holder := effect.Action{
		Key: "holder-key", Resource: pushEffectResource(pr, pr.HeadBranch),
		Payload: []byte("holder"), RetrySafety: effect.RetrySafetyUnsafe,
	}
	if outcome, _, _, err := st.SideEffects().Acquire(ctx, holder, time.Minute); err != nil ||
		outcome != effect.OutcomeApplied {
		t.Fatalf("预置资源占用失败: outcome=%v err=%v", outcome, err)
	}

	task := &storage.Task{ID: "t-push-contended"}
	findings := []domain.Finding{
		{File: "a.go", Line: 1, Verified: true, SuggestedFix: "line-one-fixed"},
	}
	r.pushVerifiedFixes(ctx, task, pr, dir, findings, 1)

	if task.FixStatus != "push_skipped" {
		t.Fatalf("被副作用闸挡住应落 push_skipped，实得 %q", task.FixStatus)
	}
	out, err := exec.Command("git", "-C", dir, "rev-list", "--count", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "1\n" {
		t.Fatalf("被挡住不得产生新提交: rev-list=%q", out)
	}
}
