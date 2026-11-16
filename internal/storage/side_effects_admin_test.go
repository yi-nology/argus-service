// side_effects_admin_test.go —— 控制面存储面（#48）：活跃租约清单的过滤/排序/
// 批量，强制释放的「清资源 + 清意图、不动 status/result」边界，以及资源行
// 不存在时的 ErrNotFound。
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/sideeffect"
)

// seedLease 造一条持有中的动作（资源闸 + Key 级执行意图同时成立）。
func seedLease(t *testing.T, st *SideEffectStore, key, resource string, ttl time.Duration) (sideeffect.Action, sideeffect.Lease) {
	t.Helper()
	act := sideeffect.Action{
		Key: sideeffect.Key(key), Resource: resource,
		Payload: []byte("payload-" + key), RetrySafety: sideeffect.RetrySafetyUnknown,
	}
	outcome, lease, _, err := st.Acquire(context.Background(), act, ttl)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(%s) outcome=%v err=%v", key, outcome, err)
	}
	return act, lease
}

func TestListActiveLeasesFiltersOrdersAndLimits(t *testing.T) {
	st := openTestStore(t).SideEffects()
	clock := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st.UseClock(func() time.Time { return clock })

	_, leaseA := seedLease(t, st, "k-a", "res-a", 10*time.Minute)
	clock = clock.Add(time.Minute)
	_, leaseB := seedLease(t, st, "k-b", "res-b", 10*time.Minute)
	clock = clock.Add(time.Minute)
	// 过期租约（1 分钟 TTL，时钟再前进 2 分钟）。
	seedLease(t, st, "k-c", "res-c", time.Minute)
	clock = clock.Add(2 * time.Minute)
	// 空闲资源行：holder 已清空，不应出现在活跃清单。
	actD, leaseD := seedLease(t, st, "k-d", "res-d", 10*time.Minute)
	if _, err := st.Complete(context.Background(), actD, leaseD, sideeffect.Result{Payload: []byte("ok")}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	rows, err := st.admin().ListActiveLeases(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Resource != "res-a" || rows[1].Resource != "res-b" {
		t.Fatalf("ListActiveLeases = %+v, want [res-a res-b]（按 lease_until 升序、排除过期/空闲）", rows)
	}
	if rows[0].Holder != leaseA.Holder || rows[1].Holder != leaseB.Holder {
		t.Errorf("holder 投影错: %+v", rows)
	}
	if rows[0].Fence != int64(leaseA.Fence) || rows[1].Fence != int64(leaseB.Fence) {
		t.Errorf("fence 投影错: %+v", rows)
	}
	if rows[0].LeaseUntil == nil || rows[0].UpdatedAt.IsZero() {
		t.Errorf("lease_until/updated_at 投影缺失: %+v", rows[0])
	}

	// limit 生效；limit<=0 回落缺省 50；超过上限钳到 200（两行场景仍全量）。
	if rows, err = st.admin().ListActiveLeases(context.Background(), 1); err != nil || len(rows) != 1 {
		t.Fatalf("limit=1 rows=%+v err=%v", rows, err)
	}
	if rows, err = st.admin().ListActiveLeases(context.Background(), 0); err != nil || len(rows) != 2 {
		t.Fatalf("limit=0 rows=%d err=%v, want 2（缺省 50）", len(rows), err)
	}
	if rows, err = st.admin().ListActiveLeases(context.Background(), 1000); err != nil || len(rows) != 2 {
		t.Fatalf("limit=1000 rows=%d err=%v, want 2（上限 200）", len(rows), err)
	}
}

func TestForceReleaseResourceClearsIntentPreservesFacts(t *testing.T) {
	store := openTestStore(t)
	st := store.SideEffects()
	ctx := context.Background()
	clock := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	st.UseClock(func() time.Time { return clock })

	// done 事实：Complete 已清意图；强制释放不得把 result 清掉或让它复活。
	actDone, leaseDone := seedLease(t, st, "k-done", "res-x", 10*time.Minute)
	if _, err := st.Complete(ctx, actDone, leaseDone, sideeffect.Result{Payload: []byte(`{"comment_id":7}`)}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// uncertain 事实 + 对账占用：释放只清占用，不替 reconciler 裁决 status。
	actUnknown, leaseUnknown := seedLease(t, st, "k-unknown", "res-x", 10*time.Minute)
	if err := st.MarkUncertain(ctx, actUnknown, leaseUnknown, "boom"); err != nil {
		t.Fatalf("MarkUncertain: %v", err)
	}
	if err := st.Release(ctx, actUnknown, leaseUnknown); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := st.BeginReconcile(ctx, actUnknown, time.Minute); err != nil {
		t.Fatalf("BeginReconcile: %v", err)
	}
	// 在飞意图：资源闸与 Key 级执行意图都挂在 k-live 上。
	actLive, _ := seedLease(t, st, "k-live", "res-x", 10*time.Minute)

	var fenceBefore sideEffectFenceRow
	if err := store.db.Where("resource = ?", "res-x").Take(&fenceBefore).Error; err != nil {
		t.Fatal(err)
	}

	res, err := st.admin().ForceReleaseResource(ctx, "res-x", ForceReleaseInput{
		OperatorFP: "fp-test01", Reason: "持有者失联",
	})
	if err != nil {
		t.Fatalf("ForceReleaseResource: %v", err)
	}
	if res.ClearedActions != 2 {
		t.Fatalf("clearedActions = %d, want 2（k-unknown 对账占用 + k-live 执行意图；done 行不计数）", res.ClearedActions)
	}
	if res.MarkedUncertain != 0 {
		t.Fatalf("markedUncertain = %d, want 0（Unknown 动作释放后语义不变）", res.MarkedUncertain)
	}
	if res.AuditID == 0 {
		t.Fatal("强制释放未落持久化审计行")
	}
	var auditRow SideEffectReleaseAudit
	if err := store.db.Where("id = ?", res.AuditID).Take(&auditRow).Error; err != nil {
		t.Fatal(err)
	}
	if auditRow.Resource != "res-x" || auditRow.ClearedActions != 2 ||
		auditRow.OperatorFP != "fp-test01" || auditRow.Reason != "持有者失联" {
		t.Errorf("审计行投影不符: %+v", auditRow)
	}

	// 资源行：holder/lease_until 清空，fence 原值保留（令牌代际继续单调）。
	var fenceAfter sideEffectFenceRow
	if err := store.db.Where("resource = ?", "res-x").Take(&fenceAfter).Error; err != nil {
		t.Fatal(err)
	}
	if fenceAfter.Holder != "" || fenceAfter.LeaseUntil != nil {
		t.Errorf("资源行未释放: holder=%q lease_until=%v", fenceAfter.Holder, fenceAfter.LeaseUntil)
	}
	if fenceAfter.Fence != fenceBefore.Fence {
		t.Errorf("fence 被改动: %d -> %d（令牌代际必须单调）", fenceBefore.Fence, fenceAfter.Fence)
	}

	// done 行：status/result 原样保留（不复活）。
	var doneRow sideEffectRow
	if err := store.db.Where("effect_key = ?", "k-done").Take(&doneRow).Error; err != nil {
		t.Fatal(err)
	}
	if doneRow.Status != sideEffectStatusDone || string(doneRow.Result) != `{"comment_id":7}` {
		t.Errorf("done 事实被改动: status=%s result=%s", doneRow.Status, doneRow.Result)
	}
	if doneRow.Holder != nil || doneRow.Fence != nil || doneRow.LeaseUntil != nil {
		t.Errorf("done 行不应有执行意图: %+v", doneRow)
	}

	// uncertain 行：status/reason 保留，对账占用清空。
	var unknownRow sideEffectRow
	if err := store.db.Where("effect_key = ?", "k-unknown").Take(&unknownRow).Error; err != nil {
		t.Fatal(err)
	}
	if unknownRow.Status != sideEffectStatusUncertain || unknownRow.Reason != "boom" {
		t.Errorf("uncertain 事实被裁决: status=%s reason=%s", unknownRow.Status, unknownRow.Reason)
	}
	if unknownRow.ReconHolder != nil || unknownRow.ReconUntil != nil {
		t.Errorf("对账占用未清: %+v", unknownRow)
	}
	// uncertain 仍应出现在待对账清单里（释放不是裁决）。
	if rows, err := st.admin().ListUncertain(ctx, clock.Add(time.Second), 10); err != nil || len(rows) != 1 || rows[0].Key != "k-unknown" {
		t.Errorf("ListUncertain after release = %+v err=%v, want [k-unknown]", rows, err)
	}

	// 在飞意图清空后同一 Key 可重新 Acquire（证明 intent 真被撤掉）；资源释放后
	// 授予应成功且令牌在旧值上继续单调（fence+1）。
	outcome, newLease, _, err := st.Acquire(ctx, actLive, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire after force release outcome=%v err=%v（执行意图未被清掉）", outcome, err)
	}
	if int64(newLease.Fence) != fenceAfter.Fence+1 {
		t.Errorf("新令牌 = %d, want %d（fence 继续单调）", newLease.Fence, fenceAfter.Fence+1)
	}

	// 不存在的资源：ErrNotFound（没有登记过就没有可释放的租约）。
	if _, err := st.admin().ForceReleaseResource(ctx, "res-missing", ForceReleaseInput{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ForceReleaseResource(missing) err=%v, want ErrNotFound", err)
	}
}

// TestForceReleaseDryRunListsAffectedWithoutWrites #64 验收：dry-run 返回受影响
func TestRequestTaskCancelStatesAndIdempotency(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	newTask := func(status string) *Task {
		task := NewTask("code_review", "gitea", "o", "r", "1")
		if err := store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if status != StatusPending {
			if err := store.db.Model(&Task{}).Where("id = ?", task.ID).Update("status", status).Error; err != nil {
				t.Fatal(err)
			}
		}
		return task
	}

	t.Run("pending_cancelled_once_then_idempotent", func(t *testing.T) {
		task := newTask(StatusPending)
		res, err := store.RequestTaskCancel(ctx, task.ID, "")
		if err != nil {
			t.Fatalf("RequestTaskCancel: %v", err)
		}
		if res.Status != StatusFailed || !res.Cancelled {
			t.Fatalf("result = %+v, want {failed true}", res)
		}
		got, err := store.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusFailed || got.CancelRequestedAt == nil || got.FinishedAt == nil {
			t.Fatalf("任务未落终止态: %+v", got)
		}
		if got.NextRetryAt != nil {
			t.Errorf("next_retry_at 应为 nil（阻断自动重试），got %v", got.NextRetryAt)
		}
		if got.FailReason != "操作员终止: operator cancel" {
			t.Errorf("fail_reason = %q", got.FailReason)
		}
		// 重复终止：幂等返回 cancelled=false，不报错。
		res, err = store.RequestTaskCancel(ctx, task.ID, "again")
		if err != nil || res.Status != StatusFailed || res.Cancelled {
			t.Fatalf("second RequestTaskCancel = %+v err=%v, want {failed false}", res, err)
		}
	})

	t.Run("running_cancelled", func(t *testing.T) {
		task := newTask(StatusRunning)
		res, err := store.RequestTaskCancel(ctx, task.ID, "生产事故")
		if err != nil || !res.Cancelled || res.Status != StatusFailed {
			t.Fatalf("running cancel = %+v err=%v", res, err)
		}
		got, _ := store.GetTask(ctx, task.ID)
		if got.FailReason != "操作员终止: 生产事故" {
			t.Errorf("fail_reason = %q", got.FailReason)
		}
	})

	t.Run("done_not_cancelable", func(t *testing.T) {
		task := newTask(StatusDone)
		if _, err := store.RequestTaskCancel(ctx, task.ID, ""); !errors.Is(err, ErrTaskNotCancelable) {
			t.Fatalf("done cancel err=%v, want ErrTaskNotCancelable", err)
		}
		got, _ := store.GetTask(ctx, task.ID)
		if got.Status != StatusDone || got.CancelRequestedAt != nil {
			t.Errorf("done 任务被改动: %+v", got)
		}
	})

	t.Run("failed_without_mark_not_cancelable", func(t *testing.T) {
		task := newTask(StatusFailed)
		if _, err := store.RequestTaskCancel(ctx, task.ID, ""); !errors.Is(err, ErrTaskNotCancelable) {
			t.Fatalf("plain failed cancel err=%v, want ErrTaskNotCancelable", err)
		}
	})

	t.Run("missing_task", func(t *testing.T) {
		if _, err := store.RequestTaskCancel(ctx, "t-missing", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing cancel err=%v, want ErrNotFound", err)
		}
	})
}

func TestTaskCancelRequested(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	task := NewTask("code_review", "gitea", "o", "r", "1")
	if err := store.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	if requested, err := store.TaskCancelRequested(ctx, task.ID); err != nil || requested {
		t.Fatalf("before cancel = %v, %v; want false, nil", requested, err)
	}
	if _, err := store.RequestTaskCancel(ctx, task.ID, ""); err != nil {
		t.Fatal(err)
	}
	if requested, err := store.TaskCancelRequested(ctx, task.ID); err != nil || !requested {
		t.Fatalf("after cancel = %v, %v; want true, nil", requested, err)
	}
	// 任务不存在：false, nil（检查点是尽力而为的协作停止）。
	if requested, err := store.TaskCancelRequested(ctx, "t-missing"); err != nil || requested {
		t.Fatalf("missing = %v, %v; want false, nil", requested, err)
	}
}
