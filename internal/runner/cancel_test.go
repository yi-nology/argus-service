// cancel_test.go —— 操作员终止的 runner 侧（#48）：检查点命中/未命中/DB 错误
// fail-open，以及 handleRunFailure 对 ErrTaskCancelled 不写终态（控制面负责）。
package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/storage"
)

func newCancelTestRunner(t *testing.T) (*Runner, *storage.Store) {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Runner{Store: st, Log: logx.Nop()}, st
}

func TestCheckCancelled(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if err := r.checkCancelled(ctx, task); err != nil {
		t.Fatalf("未终止任务 checkCancelled = %v, want nil", err)
	}
	if _, err := st.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.checkCancelled(ctx, task); !errors.Is(err, ErrTaskCancelled) {
		t.Fatalf("已终止任务 checkCancelled = %v, want ErrTaskCancelled", err)
	}

	// DB 错误 fail-open：检查点是协作停止，不是正确性防线；查询失败只告警。
	closed, err := storage.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	rClosed := &Runner{Store: closed, Log: logx.Nop()}
	if err := rClosed.checkCancelled(ctx, task); err != nil {
		t.Fatalf("DB 错误应 fail-open，got %v", err)
	}
}

func TestRunStopsAtCancelCheckpoint(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	// 第一个检查点在任何 prepareContext/LLM 调用之前返回；wf/budget/knobs 全 nil
	// 证明未走到后续步骤（走到会 nil panic）。
	err := run(r, ctx, task, nil, nil, time.Now(), nil, taskKnobs{})
	if !errors.Is(err, ErrTaskCancelled) {
		t.Fatalf("run on cancelled task = %v, want ErrTaskCancelled", err)
	}
}

func TestHandleRunFailureCancelledWritesNoTerminal(t *testing.T) {
	r, st := newCancelTestRunner(t)
	ctx := context.Background()
	task := storage.NewTask("code_review", "gitea", "o", "r", "1")
	if err := st.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := st.ClaimNextPending(ctx)
	if err != nil || !ok {
		t.Fatalf("ClaimNextPending ok=%v err=%v", ok, err)
	}

	// 控制面已置 failed 的语义由 DB 状态承担；runner 收到 ErrTaskCancelled 时
	// 不得再写终态（这里任务仍是 running，断言调用后仍 running、无 finished_at）。
	r.handleRunFailure(ctx, claimed, nil, fmt.Errorf("执行中断: %w", ErrTaskCancelled))

	got, err := st.GetTask(ctx, claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != storage.StatusRunning {
		t.Fatalf("status = %q, want running（ErrTaskCancelled 不得写终态）", got.Status)
	}
	if got.FinishedAt != nil || got.FailReason != "" {
		t.Fatalf("终态字段被写入: finished_at=%v fail_reason=%q", got.FinishedAt, got.FailReason)
	}
}
