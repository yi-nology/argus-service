// effect_prepared_test.go —— DoPrepared（#62）的闸语义单测：Prepare 失败 =
// 未施加任何副作用（Abandon，不落未知态）；Prepare 产出透传 Apply；
// direct（闸关闭）路径 Prepare 先行、失败即止。
package effect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oekit/logx"
	"github.com/oagentkit/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/storage"
)

var errPrepBoom = errors.New("prepare boom")

func prepAction() Action {
	return Action{Key: "k-prepared", Resource: "branch:gitea:o/r:b",
		Payload: []byte("p"), RetrySafety: RetrySafetyUnsafe}
}

// Prepare 失败：动作未施加（apply 零调用），错误原样上抛、非 RetryBlocked
// （不落「结果未知」——Abandon 语义）。
func TestDoPreparedPrepareFailsAbortsWithoutApply(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/prep.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := NewFromStore(st, time.Minute, logx.Nop(), true)

	applied := false
	outcome, _, err := r.DoPrepared(context.Background(), prepAction(),
		func(context.Context) (any, error) { return nil, errPrepBoom },
		func(context.Context, any, Lease) (Result, error) {
			applied = true
			return Result{}, nil
		})
	if !errors.Is(err, errPrepBoom) {
		t.Fatalf("Prepare 失败应原样上抛, got %v", err)
	}
	if applied {
		t.Fatal("Prepare 失败不得施加")
	}
	if outcome == OutcomeUncertain || RetryBlocked(err) {
		t.Fatalf("Prepare 失败不是结果未知: outcome=%v retryBlocked=%v", outcome, RetryBlocked(err))
	}
}

// Prepare 产出透传 Apply（计划对象跨阶段可见）。
func TestDoPreparedPassesPreparedToApply(t *testing.T) {
	st, err := storage.Open(t.TempDir() + "/prep2.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := NewFromStore(st, time.Minute, logx.Nop(), true)
	act := prepAction()

	got := ""
	outcome, _, err := r.DoPrepared(context.Background(), act,
		func(context.Context) (any, error) { return "plan-token", nil },
		func(_ context.Context, prepared any, _ Lease) (Result, error) {
			got, _ = prepared.(string)
			return Result{Payload: []byte("done")}, nil
		})
	if err != nil || outcome != OutcomeApplied {
		t.Fatalf("outcome=%v err=%v, want applied", outcome, err)
	}
	if got != "plan-token" {
		t.Fatalf("prepared 透传失败: %q", got)
	}
	// 再执行同 Key：replayed（Prepare 不再跑——apply 零调用由 replay 保证）
	prepRan := false
	outcome, _, err = r.DoPrepared(context.Background(), act,
		func(context.Context) (any, error) { prepRan = true; return nil, nil },
		func(context.Context, any, Lease) (Result, error) {
			return Result{Payload: []byte("again")}, nil
		})
	if err != nil || outcome != OutcomeReplayed {
		t.Fatalf("二次执行应 replay: outcome=%v err=%v", outcome, err)
	}
	if prepRan {
		t.Fatal("replayed 不得再跑 Prepare")
	}
}

// direct（闸关闭）：Prepare 先行、失败即止（fail-soft 路径同样不裸 apply）。
func TestDoPreparedDirectModePrepareGatesApply(t *testing.T) {
	r := New(nil, time.Minute, logx.Nop(), false)

	applied := false
	_, _, err := r.DoPrepared(context.Background(), prepAction(),
		func(context.Context) (any, error) { return nil, errPrepBoom },
		func(context.Context, any, Lease) (Result, error) {
			applied = true
			return Result{}, nil
		})
	if !errors.Is(err, errPrepBoom) || applied {
		t.Fatalf("direct 模式 Prepare 失败应即止: err=%v applied=%v", err, applied)
	}

	_, res, err := r.DoPrepared(context.Background(), prepAction(),
		func(context.Context) (any, error) { return 42, nil },
		func(_ context.Context, prepared any, _ Lease) (Result, error) {
			if prepared != 42 {
				t.Fatalf("direct 透传失败: %v", prepared)
			}
			return Result{Payload: []byte("ok")}, nil
		})
	if err != nil || string(res.Payload) != "ok" {
		t.Fatalf("direct 正常路径: err=%v res=%v", err, res)
	}
}

// kernel 路径分支：资源被占 → ErrContended（Prepare 不跑、不施加）。
func TestDoPreparedContended(t *testing.T) {
	st := openStore(t)
	if outcome, _, _, err := st.SideEffects().Acquire(context.Background(),
		sideeffect.Action{Key: "k-holder2", Resource: "res-held2", Payload: []byte("p")}, time.Minute); err != nil ||
		outcome != sideeffect.OutcomeApplied {
		t.Fatalf("占位 Acquire 失败: outcome=%v err=%v", outcome, err)
	}
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	prepRan, applied := false, false
	outcome, _, err := r.DoPrepared(context.Background(), testAction("k-contended2", "res-held2"),
		func(context.Context) (any, error) { prepRan = true; return nil, nil },
		func(context.Context, any, Lease) (Result, error) { applied = true; return Result{}, nil })
	if !errors.Is(err, ErrContended) || outcome != OutcomeContended || applied {
		t.Fatalf("contended: err=%v outcome=%v applied=%v", err, outcome, applied)
	}
	if prepRan {
		t.Fatal("contended 在闸外，不得跑 Prepare")
	}
}

// kernel 路径分支：结果未知 → ErrOutcomeUnknown（不施加、禁重试）。
func TestDoPreparedUncertain(t *testing.T) {
	st := openStore(t)
	gate := st.SideEffects()
	act := sideeffect.Action{Key: "k-unc2", Resource: "res-unc2", Payload: []byte("p"),
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
	applied := false
	outcome, _, err := r.DoPrepared(context.Background(), testAction("k-unc2", "res-unc2"),
		func(context.Context) (any, error) { return nil, nil },
		func(context.Context, any, Lease) (Result, error) { applied = true; return Result{}, nil })
	if !errors.Is(err, ErrOutcomeUnknown) || outcome != OutcomeUncertain || applied {
		t.Fatalf("uncertain: err=%v outcome=%v applied=%v", err, outcome, applied)
	}
}

// kernel 路径分支：施加失败（Unsafe）→ 未知态（applyErr 原因链保留）。
func TestDoPreparedApplyFailureTurnsUncertain(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	act := testAction("k-prep-apply-fail", "res-paf")

	var calls int
	_, _, err := r.DoPrepared(context.Background(), act,
		func(context.Context) (any, error) { return "p", nil },
		func(context.Context, any, Lease) (Result, error) {
			calls++
			return Result{}, errPrepBoom
		})
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("Unsafe 施加失败应未知态: %v", err)
	}
	if !errors.Is(err, errPrepBoom) {
		t.Fatalf("原始原因应保留在错误链: %v", err)
	}
	// 重试被未知态挡住（apply 不再执行）
	if _, _, err = r.DoPrepared(context.Background(), act,
		func(context.Context) (any, error) { return nil, nil },
		func(context.Context, any, Lease) (Result, error) {
			calls++
			return Result{}, nil
		}); !errors.Is(err, ErrOutcomeUnknown) || calls != 1 {
		t.Fatalf("未知态不得重试: err=%v calls=%d", err, calls)
	}
}

// kernel 路径分支：存储不可用 → fail-soft 直通（Prepare 重算后 apply 恰一次）。
func TestDoPreparedStoreUnavailableFailSoft(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	_ = st.Close()

	prepRuns, applied := 0, false
	outcome, _, err := r.DoPrepared(context.Background(), testAction("k-prep-closed", "res-pc"),
		func(context.Context) (any, error) { prepRuns++; return nil, nil },
		func(context.Context, any, Lease) (Result, error) {
			applied = true
			return Result{Payload: []byte("ok")}, nil
		})
	if err != nil || outcome != OutcomeApplied || !applied {
		t.Fatalf("fail-soft 直通: outcome=%v applied=%v err=%v", outcome, applied, err)
	}
	if prepRuns != 1 {
		t.Fatalf("存储故障重算 Prepare 应恰一次: %d", prepRuns)
	}
}

// 边界：inner 未装配（零值+enabled）→ no_runner 直通，Prepare 先行。
func TestDoPreparedNoInnerDirect(t *testing.T) {
	r := &Runner{enabled: true, log: logx.Nop()}
	prepRan, applied := false, false
	outcome, _, err := r.DoPrepared(context.Background(), prepAction(),
		func(context.Context) (any, error) { prepRan = true; return nil, nil },
		func(context.Context, any, Lease) (Result, error) {
			applied = true
			return Result{}, nil
		})
	if err != nil || outcome != OutcomeApplied || !applied || !prepRan {
		t.Fatalf("no_runner 直通: outcome=%v prep=%v applied=%v err=%v", outcome, prepRan, applied, err)
	}
}

// 边界：非法动作（缺资源）→ kernel 判据错误原样上抛（非降级非未知态）。
func TestDoPreparedInvalidAction(t *testing.T) {
	st := openStore(t)
	r := NewFromStore(st, DefaultTTL, logx.Nop(), true)
	act := Action{Key: "k-invalid", Resource: "", Payload: []byte("p")} // 缺资源
	_, _, err := r.DoPrepared(context.Background(), act,
		func(context.Context) (any, error) { return nil, nil },
		func(context.Context, any, Lease) (Result, error) { return Result{}, nil })
	if err == nil {
		t.Fatal("非法动作应报错")
	}
	if RetryBlocked(err) {
		t.Fatalf("非法动作是判据错误，不是未知态: %v", err)
	}
}

// 防御分支收尾：apply==nil 拒绝 / prepare==nil 兜底 / 零值 Runner·Do 直通 /
// New nil-log 兜底——单测钉住 fail-fast 语义。
func TestDoPreparedAndDoDefensiveBranches(t *testing.T) {
	// apply==nil（Do 与 DoPrepared 都必须显式拒绝）
	r0 := NewFromStore(openStore(t), DefaultTTL, logx.Nop(), true)
	if _, _, err := r0.Do(context.Background(), testAction("k-d0", "r-d0"), nil); err == nil {
		t.Fatal("Do apply==nil 应拒绝")
	}
	if _, _, err := r0.DoPrepared(context.Background(), testAction("k-d0", "r-d0"),
		nil, nil); err == nil {
		t.Fatal("DoPrepared apply==nil 应拒绝")
	}

	// prepare==nil：默认无准备阶段直进 apply
	applied := false
	outcome, _, err := r0.DoPrepared(context.Background(), testAction("k-pnil", "r-pnil"),
		nil, func(context.Context, any, Lease) (Result, error) { applied = true; return Result{}, nil })
	if err != nil || outcome != OutcomeApplied || !applied {
		t.Fatalf("prepare==nil 应兜底直进: outcome=%v err=%v", outcome, err)
	}

	// 零值 Runner 的 Do：no_runner fail-soft 直通
	var zero Runner
	if _, _, err := zero.Do(context.Background(), testAction("k-zero", "r-zero"),
		func(context.Context, Lease) (Result, error) { return Result{}, nil }); err != nil {
		t.Fatalf("零值 Runner Do 应直通: %v", err)
	}

	// New 的 nil log 兜底（不 panic 即可）
	if r := New(nil, time.Minute, nil, true); r == nil {
		t.Fatal("New 应返回非 nil Runner")
	}
}
