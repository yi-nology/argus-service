// effect_test.go —— fail-soft 外壳的五条出口与开关语义。
package effect

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

func openStore(t *testing.T) *storage.Store {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "effect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func testAction(key, resource string) Action {
	return Action{
		Key: Key(key), Resource: resource, Payload: []byte("p"),
		RetrySafety: RetrySafetyUnsafe,
	}
}

// 首次施加 → Applied；同 Key 再来 → Replayed 且不重复施加。
func TestDoReplaysCompletedEffect(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	var calls atomic.Int32
	apply := func(context.Context, Lease) (Result, error) {
		calls.Add(1)
		return Result{Payload: []byte(`{"comment_id":7}`)}, nil
	}

	outcome, res, err := r.Do(context.Background(), testAction("k1", "res-a"), apply)
	if err != nil || outcome != OutcomeApplied {
		t.Fatalf("首次应 applied: outcome=%v err=%v", outcome, err)
	}
	if string(res.Payload) != `{"comment_id":7}` {
		t.Fatalf("应返回施加结果: %q", res.Payload)
	}
	outcome, res, err = r.Do(context.Background(), testAction("k1", "res-a"), apply)
	if err != nil || outcome != OutcomeReplayed {
		t.Fatalf("同 Key 二次应 replayed: outcome=%v err=%v", outcome, err)
	}
	if string(res.Payload) != `{"comment_id":7}` {
		t.Fatalf("重放应取回既有结果: %q", res.Payload)
	}
	if calls.Load() != 1 {
		t.Fatalf("施加只能发生一次: %d", calls.Load())
	}
}

// 开关关闭 → 每次直通施加（不承诺 at-most-once）。
func TestDoDisabledBypassesGate(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), false)
	var calls atomic.Int32
	apply := func(context.Context, Lease) (Result, error) {
		calls.Add(1)
		return Result{}, nil
	}
	for range 2 {
		outcome, _, err := r.Do(context.Background(), testAction("k-off", "res-off"), apply)
		if err != nil || outcome != OutcomeApplied {
			t.Fatalf("关闭开关应直通 applied: outcome=%v err=%v", outcome, err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("关闭开关每次都应施加: %d", calls.Load())
	}
}

// 存储不可用（nil / 已关闭）→ fail-soft 直通，不阻断创作主链。
func TestDoFailSoftOnStoreUnavailable(t *testing.T) {
	t.Run("nil_store", func(t *testing.T) {
		var calls atomic.Int32
		r := NewFromStore(nil, DefaultTTL, logx.Nop(), true)
		outcome, _, err := r.Do(context.Background(), testAction("k-nil", "res-nil"),
			func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, nil })
		if err != nil || outcome != OutcomeApplied || calls.Load() != 1 {
			t.Fatalf("nil store 应 fail-soft 直通: outcome=%v calls=%d err=%v", outcome, calls.Load(), err)
		}
	})
	t.Run("closed_store", func(t *testing.T) {
		st := openStore(t)
		r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
		_ = st.Close()
		var calls atomic.Int32
		outcome, _, err := r.Do(context.Background(), testAction("k-closed", "res-closed"),
			func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, nil })
		if err != nil || outcome != OutcomeApplied || calls.Load() != 1 {
			t.Fatalf("存储故障应 fail-soft 直通: outcome=%v calls=%d err=%v", outcome, calls.Load(), err)
		}
	})
}

// 资源被他人持有 → ErrContended，且**不施加**。
func TestDoContendedDoesNotApply(t *testing.T) {
	st := openStore(t)
	gate := st.SideEffects()
	if outcome, _, _, err := gate.Acquire(context.Background(),
		sideeffect.Action{Key: "k-holder", Resource: "res-held", Payload: []byte("p")}, time.Minute); err != nil || outcome != sideeffect.OutcomeApplied {
		t.Fatalf("占位 Acquire 失败: outcome=%v err=%v", outcome, err)
	}
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	var calls atomic.Int32
	outcome, _, err := r.Do(context.Background(), testAction("k-contended", "res-held"),
		func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, nil })
	if !errors.Is(err, ErrContended) {
		t.Fatalf("应返回可重试的 ErrContended，实得 %v", err)
	}
	if outcome != OutcomeContended || calls.Load() != 0 {
		t.Fatalf("被占时不得施加: outcome=%v calls=%d", outcome, calls.Load())
	}
}

// 结果未知 → ErrOutcomeUnknown，且不得重试。
func TestDoUncertainBlocksApply(t *testing.T) {
	st := openStore(t)
	gate := st.SideEffects()
	act := sideeffect.Action{Key: "k-unc", Resource: "res-unc", Payload: []byte("p"),
		RetrySafety: sideeffect.RetrySafetyUnsafe}
	_, lease, _, err := gate.Acquire(context.Background(), act, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.MarkUncertain(context.Background(), act, lease, "响应丢了"); err != nil {
		t.Fatal(err)
	}
	if err := gate.Release(context.Background(), act, lease); err != nil {
		t.Fatal(err)
	}
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	var calls atomic.Int32
	outcome, _, err := r.Do(context.Background(), testAction("k-unc", "res-unc"),
		func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, nil })
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("未知态应返回 ErrOutcomeUnknown，实得 %v", err)
	}
	if outcome != OutcomeUncertain || calls.Load() != 0 {
		t.Fatalf("未知态不得施加: outcome=%v calls=%d", outcome, calls.Load())
	}
}

// 施加失败（Unsafe）→ 未知态；重试仍被拒，施加只发生一次。
func TestDoApplyFailureTurnsUncertain(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	var calls atomic.Int32
	boom := errors.New("provider 500")
	apply := func(context.Context, Lease) (Result, error) {
		calls.Add(1)
		return Result{}, boom
	}
	outcome, _, err := r.Do(context.Background(), testAction("k-fail", "res-fail"), apply)
	if !errors.Is(err, ErrOutcomeUnknown) && !errors.Is(err, boom) {
		t.Fatalf("施加失败应进未知态（错误链含 ErrOutcomeUnknown 或原始原因），实得 %v", err)
	}
	if outcome != OutcomeUncertain {
		t.Fatalf("Unsafe 施加失败应为 uncertain，实得 %v", outcome)
	}
	if _, _, err := r.Do(context.Background(), testAction("k-fail", "res-fail"), apply); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("未知态重试必须被拒为 ErrOutcomeUnknown，实得 %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("重试不得重复施加: %d", calls.Load())
	}
}

// 零 TTL 是装配错误 → 拒绝执行（不 fail-soft 掩盖）。
func TestDoRejectsInvalidTTL(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, 0, logx.Nop(), true)
	var calls atomic.Int32
	_, _, err := r.Do(context.Background(), testAction("k-ttl", "res-ttl"),
		func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, nil })
	if err == nil {
		t.Fatal("零 TTL 应显式失败")
	}
	if calls.Load() != 0 {
		t.Fatalf("非法动作不得施加: %d", calls.Load())
	}
}

// 开关词表：缺省 on；off/0/false/disabled 绕过（大小写不敏感）。
func TestEnabledEnv(t *testing.T) {
	cases := map[string]bool{
		"": true, "on": true, "ON": true, "1": true,
		"off": false, "OFF": false, "0": false, "false": false, "disabled": false,
	}
	for raw, want := range cases {
		t.Setenv(EnvMode, raw)
		if got := Enabled(); got != want {
			t.Errorf("Enabled(%q) = %v, want %v", raw, got, want)
		}
	}
}

// RetryBlocked 只认「禁止盲目重试」一族。
func TestRetryBlocked(t *testing.T) {
	blocked := []error{
		ErrContended, ErrOutcomeUnknown, ErrLeaseLost,
		sideeffect.ErrFenceStale, sideeffect.ErrNotHolder,
		sideeffect.ErrResourceMismatch, sideeffect.ErrActionConflict,
	}
	for _, err := range blocked {
		if !RetryBlocked(err) {
			t.Errorf("%v 应被视为禁止盲目重试", err)
		}
	}
	if RetryBlocked(errors.New("普通平台错误")) {
		t.Fatal("普通错误不应被视为禁止重试")
	}
}

// Safe 动作的执行体失败不得被误判成存储故障（否则 fail-soft 会再施加一次）。
func TestDoSafeApplyFailureIsNotRetried(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	act := testAction("k-safe-fail", "res-safe-fail")
	act.RetrySafety = RetrySafetySafe
	var calls atomic.Int32
	boom := errors.New("provider 500")
	outcome, _, err := r.Do(context.Background(), act,
		func(context.Context, Lease) (Result, error) { calls.Add(1); return Result{}, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("执行体失败应原样返回: %v", err)
	}
	if outcome != OutcomeContended || calls.Load() != 1 {
		t.Fatalf("Safe 失败不得 fail-soft 重放: outcome=%v calls=%d", outcome, calls.Load())
	}
}

// 语义哨兵是判据不是故障：绝不降级成 fail-soft 直通（会绕过 at-most-once）。
func TestStorageFailureSemanticSentinelsDoNotDegrade(t *testing.T) {
	semantic := []error{
		sideeffect.ErrOutcomeUnknown,
		sideeffect.ErrLeaseLost,
		sideeffect.ErrFenceStale,
		sideeffect.ErrNotHolder,
		sideeffect.ErrResourceMismatch,
		sideeffect.ErrActionConflict,
		sideeffect.ErrInvalidAction,
		sideeffect.ErrUnknownOutcome,
		context.Canceled,
		context.DeadlineExceeded,
	}
	for _, err := range semantic {
		if storageFailure(err) {
			t.Errorf("%v 是语义判据，不得降级", err)
		}
	}
	if !storageFailure(sideeffect.ErrNoStore) {
		t.Error("ErrNoStore 属于存储不可用，应降级")
	}
	if !storageFailure(errors.New("database is locked")) {
		t.Error("未识别的原始存储错误应降级")
	}
}
