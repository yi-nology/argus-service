// side_effects_reconcile_test.go —— 宿主扩展（行式对账）的存储面测试：
// ListUncertain 的过滤/排序/批量，ResolveUncertainKey 的四类出口
// （已生效→重放、未生效→可重试/被拒、非未知态、Key 不存在）。
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oagentkit/goagent-kernel/sideeffect"
)

// seedUncertain 造一条 unknown 态记录：Acquire → MarkUncertain → Release 资源闸
// （MarkUncertain 只撤执行意图，资源由调用方显式释放；与 poster 的真实时序一致）。
func seedUncertain(t *testing.T, st *SideEffectStore, key, resource string,
	safety sideeffect.RetrySafety) sideeffect.Action {
	t.Helper()
	act := sideeffect.Action{
		Key: sideeffect.Key(key), Resource: resource,
		Payload: []byte("payload-" + key), RetrySafety: safety,
	}
	ctx := context.Background()
	outcome, lease, _, err := st.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(%s) outcome=%v err=%v", key, outcome, err)
	}
	if err := st.MarkUncertain(ctx, act, lease, "boom-"+key); err != nil {
		t.Fatalf("MarkUncertain(%s): %v", key, err)
	}
	if err := st.Release(ctx, act, lease); err != nil {
		t.Fatalf("Release(%s): %v", key, err)
	}
	return act
}

func TestListUncertainFiltersOrdersAndLimits(t *testing.T) {
	st := openTestStore(t).SideEffects()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	st.UseClock(func() time.Time { return clock })

	seedUncertain(t, st, "k-a", "res-a", sideeffect.RetrySafetyUnsafe)
	clock = clock.Add(time.Minute)
	seedUncertain(t, st, "k-b", "res-b", sideeffect.RetrySafetySafe)
	clock = clock.Add(time.Minute)
	seedUncertain(t, st, "k-c", "res-c", sideeffect.RetrySafetyUnknown)

	// 非 uncertain 行：已 done 与 pending 都不得出现在结果里。
	done := sideeffect.Action{Key: "k-done", Resource: "res-done", Payload: []byte("p")}
	outcome, lease, _, err := st.Acquire(context.Background(), done, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(done) outcome=%v err=%v", outcome, err)
	}
	if _, err := st.Complete(context.Background(), done, lease, sideeffect.Result{Payload: []byte("v")}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	pending := sideeffect.Action{Key: "k-pending", Resource: "res-pending", Payload: []byte("p")}
	if outcome, _, _, err := st.Acquire(context.Background(), pending, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire(pending) outcome=%v err=%v", outcome, err)
	}

	// cutoff = k-c 的 updated_at：严格小于，k-c 不入选；k-a/k-b 按龄升序。
	rows, err := st.admin().ListUncertain(context.Background(), clock, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Key != "k-a" || rows[1].Key != "k-b" {
		t.Fatalf("ListUncertain = %+v, want [k-a k-b]", rows)
	}
	if rows[0].Resource != "res-a" || rows[0].RetrySafety != int16(sideeffect.RetrySafetyUnsafe) ||
		rows[0].Reason != "boom-k-a" {
		t.Errorf("行投影错: %+v", rows[0])
	}
	if want := sideeffect.HashPayload([]byte("payload-k-a")); rows[0].PayloadHash != want {
		t.Errorf("payload_hash = %q, want %q", rows[0].PayloadHash, want)
	}
	if !rows[0].UpdatedAt.UTC().Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("updated_at = %v, want 2026-01-01T00:00:00Z", rows[0].UpdatedAt)
	}

	// limit 生效；limit<=0 回落缺省批量（不截断这两行）。
	if rows, err = st.admin().ListUncertain(context.Background(), clock, 1); err != nil {
		t.Fatal(err)
	} else if len(rows) != 1 || rows[0].Key != "k-a" {
		t.Fatalf("limit=1 rows = %+v", rows)
	}
	if rows, err = st.admin().ListUncertain(context.Background(), clock, 0); err != nil {
		t.Fatal(err)
	} else if len(rows) != 2 {
		t.Fatalf("limit=0 rows = %d, want 2（缺省 50）", len(rows))
	}
}

func TestResolveUncertainKeyAppliedReplays(t *testing.T) {
	st := openTestStore(t).SideEffects()
	act := seedUncertain(t, st, "k-applied", "res-applied", sideeffect.RetrySafetyUnsafe)
	payload := []byte(`{"comment_id":42}`)

	outcome, res, err := st.admin().ResolveUncertainKey(context.Background(), "k-applied",
		sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: payload}, Reason: "marker found"})
	if err != nil || outcome != sideeffect.OutcomeReplayed || string(res.Payload) != string(payload) {
		t.Fatalf("裁决已生效: outcome=%v res=%q err=%v", outcome, res.Payload, err)
	}

	// 已完成后再裁决：重放既有结果，不覆盖。
	outcome, res, err = st.admin().ResolveUncertainKey(context.Background(), "k-applied",
		sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: []byte(`{"comment_id":9}`)}})
	if err != nil || outcome != sideeffect.OutcomeReplayed || string(res.Payload) != string(payload) {
		t.Fatalf("已完成重放: outcome=%v res=%q err=%v", outcome, res.Payload, err)
	}

	// 后续 Acquire 取回同一结果，不重新授予执行权。
	outcome, _, res, err = st.Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeReplayed || string(res.Payload) != string(payload) {
		t.Fatalf("Acquire after resolve: outcome=%v res=%q err=%v", outcome, res.Payload, err)
	}
}

func TestResolveUncertainKeyNotAppliedRetryable(t *testing.T) {
	st := openTestStore(t).SideEffects()
	act := seedUncertain(t, st, "k-retry", "res-retry", sideeffect.RetrySafetyUnsafe)

	outcome, _, err := st.admin().ResolveUncertainKey(context.Background(), "k-retry",
		sideeffect.Resolution{Applied: false, ForceRetryable: true, Reason: "marker absent"})
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("裁决未生效应重新可执行: outcome=%v err=%v", outcome, err)
	}
	outcome, _, _, err = st.Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("裁决后可重新 Acquire: outcome=%v err=%v", outcome, err)
	}
}

func TestResolveUncertainKeyUnsafeWithoutForceStaysBlocked(t *testing.T) {
	st := openTestStore(t).SideEffects()
	act := seedUncertain(t, st, "k-unsafe", "res-unsafe", sideeffect.RetrySafetyUnsafe)

	_, _, err := st.admin().ResolveUncertainKey(context.Background(), "k-unsafe",
		sideeffect.Resolution{Applied: false, Reason: "marker absent"})
	if !errors.Is(err, sideeffect.ErrUnsafeNotRetryable) {
		t.Fatalf("Unsafe 未生效裁决应被拒: err=%v", err)
	}
	outcome, _, _, err := st.Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != sideeffect.OutcomeUncertain {
		t.Fatalf("拒绝后必须保持未知态: outcome=%v err=%v", outcome, err)
	}
}

func TestResolveUncertainKeyRejectsNonUncertainAndMissing(t *testing.T) {
	st := openTestStore(t).SideEffects()
	act := sideeffect.Action{Key: "k-pending", Resource: "res-pending", Payload: []byte("p")}
	if outcome, _, _, err := st.Acquire(context.Background(), act, time.Minute); err != nil ||
		outcome != sideeffect.OutcomeApplied {
		t.Fatalf("Acquire: outcome=%v err=%v", outcome, err)
	}

	if _, _, err := st.admin().ResolveUncertainKey(context.Background(), "k-pending",
		sideeffect.Resolution{Applied: true}); !errors.Is(err, sideeffect.ErrNotUncertain) {
		t.Fatalf("pending 应 ErrNotUncertain: %v", err)
	}
	if _, _, err := st.admin().ResolveUncertainKey(context.Background(), "k-missing",
		sideeffect.Resolution{Applied: true}); !errors.Is(err, sideeffect.ErrNotUncertain) {
		t.Fatalf("不存在的 Key 应 ErrNotUncertain: %v", err)
	}
	if _, _, err := st.admin().ResolveUncertainKey(context.Background(), "",
		sideeffect.Resolution{Applied: true}); !errors.Is(err, sideeffect.ErrNotUncertain) {
		t.Fatalf("空 Key 应 ErrNotUncertain: %v", err)
	}
}
