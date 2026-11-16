package bootstrap

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/storage"
)

// runOnceUnderLease：抢到租约才执行、跑完释放；他人持有让位；故障降级执行。
func TestRunOnceUnderLease(t *testing.T) {
	st, err := storage.Open(filepath.Join(t.TempDir(), "lease.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	key, holder := "bootstrap-test/lease", "holder-a"
	calls := 0
	fn := func(context.Context) { calls++ }

	runOnceUnderLease(ctx, st, key, holder, time.Minute, fn)
	if calls != 1 {
		t.Fatalf("free lease: calls=%d, want 1", calls)
	}
	// 跑完即释放：同 holder 下一次还能抢到。
	runOnceUnderLease(ctx, st, key, holder, time.Minute, fn)
	if calls != 2 {
		t.Fatalf("released lease: calls=%d, want 2", calls)
	}

	// 他副本持有未过期：本次必须让位。
	if ok, err := st.TryAcquire(ctx, key, "holder-b", time.Minute); err != nil || !ok {
		t.Fatalf("pre-acquire: ok=%v err=%v", ok, err)
	}
	runOnceUnderLease(ctx, st, key, holder, time.Minute, fn)
	if calls != 2 {
		t.Fatalf("held lease: calls=%d, want 2（不得双跑）", calls)
	}
	if err := st.Release(ctx, key, "holder-b"); err != nil {
		t.Fatalf("release: %v", err)
	}
	runOnceUnderLease(ctx, st, key, holder, time.Minute, fn)
	if calls != 3 {
		t.Fatalf("after release: calls=%d, want 3", calls)
	}
}

// setupPIIPolicy 开关两个分支：开=装配 mask；关=不改写策略但不 panic。
func TestSetupPIIPolicyToggles(t *testing.T) {
	setupPIIPolicy(&config.Config{PIIMask: true}, logx.Nop())
	setupPIIPolicy(&config.Config{PIIMask: false}, logx.Nop())
}
