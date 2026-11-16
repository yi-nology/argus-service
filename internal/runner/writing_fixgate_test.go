// writing_fixgate_test.go —— #51 修复链收口回归：push 链所有退出路径必须落
// **定义内** FixStatus 终态，不留在瞬态 pushing。三条路径：
//  1. 归属检查拿到未定义结论（GetTask 失败）→ effect.RetryBlocked 守卫拦下
//     （此前：静默继续推送=僵尸写）→ push_failed/store_unknown_outcome；
//  2. 清场失败（非 git 目录）→ push_failed/clean_failed（此前：静默 return，
//     FixStatus 停在 pushing）；
//  3. 无可推送内容/缺目标分支 → skipped_noop（此前：静默 return，停在 pushing）。
package runner

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// fixGateRunner 带真 sqlite Store 的 Runner（push 链触达 Store+Log；Audit nil
// 安全；direct 形态不触 SDK）。
func fixGateRunner(t *testing.T) (*Runner, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "fixgate.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Runner{
		Cfg:   &config.Config{FixPush: true, FixPushMode: "direct"},
		Log:   logx.Nop(),
		Store: store,
	}, store
}

// fixGateTask 落一个 running 任务（归属检查通过的前提）。
func fixGateTask(t *testing.T, store *storage.Store) *storage.Task {
	t.Helper()
	task := storage.NewTask("review", "github", "acme", "pay-gateway", "42")
	task.Status = storage.StatusRunning
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("建任务失败: %v", err)
	}
	return task
}

// TestFixPushStoreUnknownOutcomeGated 归属检查拿到未定义结论（任务 ID 不在
// 库=裁决者失明）→ 不继续推送链，显式落 push_failed/store_unknown_outcome。
// 反证锚点：pr.HeadBranch=""——若守卫失效（旧行为静默继续），流程会走到
// noop 路径落 skipped_noop，状态可分辨。
func TestFixPushStoreUnknownOutcomeGated(t *testing.T) {
	r, store := fixGateRunner(t)
	fixGateTask(t, store) // 库里有任务，但推送持有的是**另一个** ID → GetTask 失明

	ghost := &storage.Task{ID: "t-doesnotexist000000000000", Status: storage.StatusRunning}
	pr := &plugin.PRContext{Platform: "github", Owner: "acme", Repo: "pay-gateway",
		Number: "42", HeadBranch: ""}

	r.pushVerifiedFixes(context.Background(), ghost, pr, t.TempDir(), nil, 1)

	if ghost.FixStatus != "push_failed" {
		t.Fatalf("FixStatus = %q, want push_failed（未知结论必须被守卫拦下）", ghost.FixStatus)
	}
}

// TestFixPushCleanFailureLandsTerminal 清场失败（目录非 git 仓）→ 落
// push_failed/clean_failed，不留在瞬态 pushing。
func TestFixPushCleanFailureLandsTerminal(t *testing.T) {
	r, store := fixGateRunner(t)
	task := fixGateTask(t, store)
	pr := &plugin.PRContext{Platform: "github", Owner: "acme", Repo: "pay-gateway",
		Number: "42", HeadBranch: "feature/login"}

	r.pushVerifiedFixes(context.Background(), task, pr, t.TempDir(), nil, 1) // 空 dir 非 git 仓

	if task.FixStatus != "push_failed" {
		t.Fatalf("FixStatus = %q, want push_failed（清场失败必须落终态，不得停在 pushing）", task.FixStatus)
	}
}

// TestFixPushNoopLandsTerminal 无目标分支（HeadBranch=""）→ 落 skipped_noop，
// 不留在瞬态 pushing。真 git 仓 + 空 findings：清场与 Apply 都成功，
// 走到 noop 短路点。
func TestFixPushNoopLandsTerminal(t *testing.T) {
	r, store := fixGateRunner(t)
	task := fixGateTask(t, store)
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"git", "init", "-q", dir},
		{"git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t.local", "commit", "--allow-empty", "-q", "-m", "init"},
	} {
		if out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git 准备失败 %v: %s", err, strings.TrimSpace(string(out)))
		}
	}
	pr := &plugin.PRContext{Platform: "github", Owner: "acme", Repo: "pay-gateway",
		Number: "42", HeadBranch: ""}

	r.pushVerifiedFixes(context.Background(), task, pr, dir, nil, 1)

	if task.FixStatus != "skipped_noop" {
		t.Fatalf("FixStatus = %q, want skipped_noop（noop 必须落终态，不得停在 pushing）", task.FixStatus)
	}
}
