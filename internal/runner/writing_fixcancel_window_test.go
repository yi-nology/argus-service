// writing_fixcancel_window_test.go —— #63 取消与外部写入的竞态窗口收紧：
// 取消复查放 effect Prepare 阶段（闸内、施加前）——取消后旧执行者不再获得
// 平台写入许可；Abandon 语义不落「结果未知」。epoch/Intent 绑定留 v7.5。
package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/yi-nology/argus/internal/storage"
)

// fixCancelled 三态：取消标记 / ctx 结束 / 正常。
func TestFixCancelled(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if fixCancelled(r, ctx, task) {
		t.Fatal("正常任务不应判取消")
	}
	if _, err := st.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if !fixCancelled(r, ctx, task) {
		t.Fatal("取消标记后应判取消")
	}
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if !fixCancelled(r, cancelledCtx, task) {
		t.Fatal("ctx 结束应判取消")
	}
}

// 取消 vs push 窗口（闸内复查，entry 检查已过、闸执行在即）：prepareFixPush
// 返回 errFixAborted，状态 skipped_stale——不带任何 git 动作。
func TestFixPushCancelInGateWindowAborts(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}

	prepared, err := prepareFixPush(r, ctx, task, nil, "", "branch", "argus/fix/42")
	if !errors.Is(err, errFixAborted) {
		t.Fatalf("闸内取消应返回 errFixAborted, got %v (prepared=%v)", err, prepared)
	}
	if task.FixStatus != "skipped_stale" {
		t.Fatalf("状态应落 skipped_stale, got %q", task.FixStatus)
	}
}

// 未取消时 prepareFixPush 正常出计划（不误伤主链路）。
func TestFixPushPrepareNotCancelled(t *testing.T) {
	st := gateStore(t)
	work, _ := initCASFixture(t)
	r := casRunner(st)
	task := &storage.Task{ID: "t-prepare-ok"}

	prepared, err := prepareFixPush(r, context.Background(), task, nil, work, "branch", "argus/fix/42")
	if err != nil {
		t.Fatalf("未取消不应报错: %v", err)
	}
	plan, ok := prepared.(fixPushPlan)
	if !ok {
		t.Fatalf("branch 模式应返回推送计划, got %T", prepared)
	}
	if len(plan.argv) == 0 {
		t.Fatal("计划应含 push argv")
	}
}

// 取消 vs 开 PR 窗口：fixCRCancelled 落 pr_skipped（分支已推，PR 创建放弃）。
func TestFixCRCancelInGateWindowSkips(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := fixCRCancelled(r, ctx, task); !errors.Is(err, errFixAborted) {
		t.Fatalf("应返回 errFixAborted, got %v", err)
	}
	if task.FixStatus != "pr_skipped" {
		t.Fatalf("状态应落 pr_skipped, got %q", task.FixStatus)
	}
}
