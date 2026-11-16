// reconcile_test.go —— 未知态自动对账的宿主行为：Applied→Replayed、
// 未生效→重新可执行、不支持→skipped 留状态、grace 过滤、单行失败不阻断。
package effect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

// seedUncertainEffect 造一条 unknown 态（真实 sqlite store，非 fake）。
func seedUncertainEffect(t *testing.T, st *storage.Store, key, resource string) Action {
	t.Helper()
	act := Action{
		Key: Key(key), Resource: resource, Payload: []byte("payload-" + key),
		RetrySafety: RetrySafetyUnsafe,
	}
	se := st.SideEffects()
	ctx := context.Background()
	outcome, lease, _, err := se.Acquire(ctx, act, time.Minute)
	if err != nil || outcome != OutcomeApplied {
		t.Fatalf("Acquire(%s): outcome=%v err=%v", key, outcome, err)
	}
	if err := se.MarkUncertain(ctx, act, lease, "boom-"+key); err != nil {
		t.Fatalf("MarkUncertain(%s): %v", key, err)
	}
	if err := se.Release(ctx, act, lease); err != nil {
		t.Fatalf("Release(%s): %v", key, err)
	}
	return act
}

// fakeResolver 按 Key 返回预设裁决/支持性/错误。
type fakeResolver struct {
	calls []string
	fn    func(u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error)
}

func (f *fakeResolver) Resolve(_ context.Context, u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
	f.calls = append(f.calls, u.Key)
	return f.fn(u)
}

func newReconcilerForTest(st *storage.Store, f *fakeResolver) *Reconciler {
	r := NewReconciler(st, f, logx.Nop())
	r.grace = 0 // 测试不等待 5m 静置窗口
	return r
}

func TestReconcileOnceAppliedMarksDone(t *testing.T) {
	st := openStore(t)
	act := seedUncertainEffect(t, st, "comment:summary:t-1:sha", "pr:github:o/r#1")
	payload := []byte(`{"comment_id":77}`)
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		return sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: payload},
			Reason: "comment marker found"}, true, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if err != nil || resolved != 1 || skipped != 0 {
		t.Fatalf("ReconcileOnce = (%d,%d,%v), want (1,0,nil)", resolved, skipped, err)
	}
	outcome, _, res, err := st.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != OutcomeReplayed || string(res.Payload) != string(payload) {
		t.Fatalf("Acquire after reconcile: outcome=%v res=%q err=%v", outcome, res.Payload, err)
	}
}

func TestReconcileOnceNotAppliedMakesRetryable(t *testing.T) {
	st := openStore(t)
	act := seedUncertainEffect(t, st, "comment:summary:t-2:sha", "pr:github:o/r#2")
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		return sideeffect.Resolution{Applied: false, ForceRetryable: true,
			Reason: "comment marker absent"}, true, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if err != nil || resolved != 1 || skipped != 0 {
		t.Fatalf("ReconcileOnce = (%d,%d,%v), want (1,0,nil)", resolved, skipped, err)
	}
	outcome, _, _, err := st.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != OutcomeApplied {
		t.Fatalf("裁决未生效后应重新可执行: outcome=%v err=%v", outcome, err)
	}
}

func TestReconcileOnceSkipsUnsupportedKey(t *testing.T) {
	st := openStore(t)
	act := seedUncertainEffect(t, st, "branch:push:t-3", "pr:github:o/r#3")
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		return sideeffect.Resolution{}, false, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if err != nil || resolved != 0 || skipped != 1 {
		t.Fatalf("ReconcileOnce = (%d,%d,%v), want (0,1,nil)", resolved, skipped, err)
	}
	outcome, _, _, err := st.SideEffects().Acquire(context.Background(), act, time.Minute)
	if err != nil || outcome != OutcomeUncertain {
		t.Fatalf("跳过的 Key 必须保持未知态: outcome=%v err=%v", outcome, err)
	}
}

func TestReconcileOnceGraceFiltersFreshRows(t *testing.T) {
	st := openStore(t)
	seedUncertainEffect(t, st, "comment:summary:t-4:sha", "pr:github:o/r#4")
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		return sideeffect.Resolution{Applied: true}, true, nil
	}}

	resolved, skipped, err := NewReconciler(st, resolver, logx.Nop()).ReconcileOnce(context.Background())
	if err != nil || resolved != 0 || skipped != 0 {
		t.Fatalf("grace 内的行不应处理: (%d,%d,%v)", resolved, skipped, err)
	}
	if len(resolver.calls) != 0 {
		t.Fatalf("resolver 不应被调用: %v", resolver.calls)
	}
}

func TestReconcileOnceRowErrorDoesNotBlockOthers(t *testing.T) {
	st := openStore(t)
	bad := seedUncertainEffect(t, st, "comment:summary:t-5:sha", "pr:github:o/r#5")
	good := seedUncertainEffect(t, st, "comment:summary:t-6:sha", "pr:github:o/r#6")
	boom := errors.New("platform unavailable")
	resolver := &fakeResolver{fn: func(u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		if u.Key == string(bad.Key) {
			return sideeffect.Resolution{}, true, boom
		}
		return sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: []byte(`{"comment_id":1}`)}}, true, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if resolved != 1 || skipped != 0 || !errors.Is(err, boom) {
		t.Fatalf("ReconcileOnce = (%d,%d,%v), want (1,0,boom)", resolved, skipped, err)
	}
	outcome, _, _, err := st.SideEffects().Acquire(context.Background(), bad, time.Minute)
	if err != nil || outcome != OutcomeUncertain {
		t.Fatalf("出错行必须保持未知态: outcome=%v err=%v", outcome, err)
	}
	outcome, _, _, err = st.SideEffects().Acquire(context.Background(), good, time.Minute)
	if err != nil || outcome != OutcomeReplayed {
		t.Fatalf("其余行应完成裁决: outcome=%v err=%v", outcome, err)
	}
}

func TestReconcileOnceConcurrentResolveIsBenign(t *testing.T) {
	st := openStore(t)
	seedUncertainEffect(t, st, "comment:summary:t-8:sha", "pr:github:o/r#8")
	resolver := &fakeResolver{fn: func(u storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		// 模拟另一个副本在查询窗口内先落结论：本副本随后裁决会撞 ErrNotUncertain。
		if _, _, err := st.SideEffects().ResolveUncertainKey(context.Background(), u.Key,
			sideeffect.Resolution{Applied: false, ForceRetryable: true}); err != nil {
			t.Fatalf("预置并发裁决失败: %v", err)
		}
		return sideeffect.Resolution{Applied: false, ForceRetryable: true}, true, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if err != nil || resolved != 0 || skipped != 1 {
		t.Fatalf("并发裁决应 benign skipped: (%d,%d,%v)", resolved, skipped, err)
	}
}

func TestReconcileOnceResolveErrorIsReported(t *testing.T) {
	st := openStore(t)
	seedUncertainEffect(t, st, "comment:summary:t-9:sha", "pr:github:o/r#9")
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		// 查询成功后存储面故障：裁决入口必须报错，不能把未知态当已裁决。
		_ = st.Close()
		return sideeffect.Resolution{Applied: true}, true, nil
	}}

	resolved, skipped, err := newReconcilerForTest(st, resolver).ReconcileOnce(context.Background())
	if err == nil || resolved != 0 || skipped != 0 {
		t.Fatalf("存储裁决失败应报错: (%d,%d,%v)", resolved, skipped, err)
	}
}

func TestNewReconcilerDefaults(t *testing.T) {
	r := NewReconciler(nil, nil, nil)
	if r.log == nil {
		t.Fatal("nil logger 应回落 logx.Nop")
	}
	if r.interval != DefaultReconcileInterval || r.grace != DefaultReconcileGrace ||
		r.batch != DefaultReconcileBatch {
		t.Fatalf("缺省值错: interval=%v grace=%v batch=%d", r.interval, r.grace, r.batch)
	}
}

func TestReconcileRejectsMissingWiring(t *testing.T) {
	if _, _, err := (&Reconciler{}).ReconcileOnce(context.Background()); err == nil {
		t.Fatal("未装配 store/resolver 必须报错")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	st := openStore(t)
	seedUncertainEffect(t, st, "comment:summary:t-7:sha", "pr:github:o/r#7")
	hit := make(chan struct{}, 1)
	resolver := &fakeResolver{fn: func(storage.UncertainSideEffect) (sideeffect.Resolution, bool, error) {
		select {
		case hit <- struct{}{}:
		default:
		}
		return sideeffect.Resolution{Applied: true, Result: sideeffect.Result{Payload: []byte(`{"comment_id":2}`)}}, true, nil
	}}
	r := NewReconciler(st, resolver, logx.Nop())
	r.grace = 0
	r.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Run(ctx)
	}()
	select {
	case <-hit:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在周期内执行对账")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 Run 未退出")
	}

	// 非正周期直接返回（不阻塞）。
	(&Reconciler{interval: 0}).Run(context.Background())
}
