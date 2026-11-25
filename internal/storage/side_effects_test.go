// side_effects_test.go —— sideeffect.Store 共享契约套件（sidetest.Run）接入。
//
// 这是本实现唯一被认可的正确性判据：33 个子测试逐条钉住 kernel 契约的失效形态
// （幂等重放 / 资源独占 / 栅栏单调 / 结果未知 / 执行意图 / 持有者身份 /
// 标记失败 / 重试安全三档 / 对账占用 / 动作身份不可变）。任何一条红 = 实现
// 违反契约，先修实现，不改套件。
package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oagentkit/goagent-kernel/sideeffect"
	"github.com/oagentkit/goagent-kernel/sidetest"
)

// TestSideEffectStoreContract 共享契约套件（33 子测试）。factory 每个子测试
// 新建一份临时库（sqlite；ARGUS_TEST_MYSQL_DSN/ARGUS_TEST_PG_DSN 时同跑对应方言）。
func TestSideEffectStoreContract(t *testing.T) {
	sidetest.Run(t, func(t *testing.T, clock *sidetest.FakeClock) sidetest.ClockedStore {
		return openTestStore(t).SideEffects()
	})
}

// TestSideEffectStoreConcurrentSameKey 同一 Key 并发抢闸：至多一个 Applied，
// 其余必须是被拒（contended/replayed），绝不能出现第二个执行权。
func TestSideEffectStoreConcurrentSameKey(t *testing.T) {
	st := openTestStore(t).SideEffects()
	act := sideeffect.Action{Key: "k-concurrent", Resource: "res-concurrent", Payload: []byte("p")}

	const workers = 16
	var applied, rejected atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcome, lease, _, err := st.Acquire(context.Background(), act, time.Minute)
			if err != nil {
				errs <- err
				return
			}
			switch outcome {
			case sideeffect.OutcomeApplied:
				applied.Add(1)
				// 持闸者立刻完成，让后续调用变成 replayed 而不是 contended。
				if _, err := st.Complete(context.Background(), act, lease, sideeffect.Result{Payload: []byte("v")}); err != nil {
					errs <- err
				}
			case sideeffect.OutcomeContended, sideeffect.OutcomeReplayed:
				rejected.Add(1)
			default:
				errs <- &unexpectedOutcome{outcome: outcome}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("并发 Acquire 失败: %v", err)
	}
	if applied.Load() != 1 {
		t.Fatalf("同一 Key 并发只能有一个执行权，实得 applied=%d rejected=%d", applied.Load(), rejected.Load())
	}
}

type unexpectedOutcome struct{ outcome sideeffect.Outcome }

func (e *unexpectedOutcome) Error() string { return "unexpected outcome: " + e.outcome.String() }

// TestSideEffectTablesExist 迁移建表哨兵：Open 后两张表必须存在。
func TestSideEffectTablesExist(t *testing.T) {
	st := openTestStore(t)
	for _, table := range []string{"side_effect_fences", "side_effects"} {
		if !st.db.Migrator().HasTable(table) {
			t.Fatalf("sideeffect 迁移缺表 %q", table)
		}
	}
}
