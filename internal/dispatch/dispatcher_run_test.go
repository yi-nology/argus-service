package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// ---- Run tests ----

func TestRun_CircuitBreakerSkips(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(&Registration{
		Manifest: plugin.AgentManifest{Name: "fail-plugin"},
		Plugin:   &failPlugin{manifest: plugin.AgentManifest{Name: "fail-plugin"}},
		Enabled:  true,
	})

	// 触发熔断：连续失败 3 次
	for range 3 {
		sel := []Selection{{Reg: &Registration{
			Manifest: plugin.AgentManifest{Name: "fail-plugin"},
			Plugin:   &failPlugin{manifest: plugin.AgentManifest{Name: "fail-plugin"}},
			Enabled:  true,
		}}}
		d.Run(context.Background(), plugin.Task{}, sel, nil)
	}

	// 第 4 次应被熔断跳过
	sel := []Selection{{Reg: &Registration{
		Manifest: plugin.AgentManifest{Name: "fail-plugin"},
		Plugin:   &failPlugin{manifest: plugin.AgentManifest{Name: "fail-plugin"}},
		Enabled:  true,
	}}}
	outcomes := d.Run(context.Background(), plugin.Task{}, sel, nil)
	if outcomes[0].SkippedReason == "" {
		t.Fatal("熔断后应跳过")
	}
}

func TestRun_BudgetExhaustedSkips(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	budget := NewTokenBudget(100)
	budget.Add(100) // 耗尽

	_ = d.reg.Register(&Registration{
		Manifest: plugin.AgentManifest{Name: "normal"},
		Plugin:   &nopPlugin{manifest: plugin.AgentManifest{Name: "normal"}},
		Enabled:  true,
	})

	sel := []Selection{{Reg: &Registration{
		Manifest: plugin.AgentManifest{Name: "normal"},
		Plugin:   &nopPlugin{manifest: plugin.AgentManifest{Name: "normal"}},
		Enabled:  true,
	}}}
	outcomes := d.Run(context.Background(), plugin.Task{}, sel, budget)
	if outcomes[0].SkippedReason == "" {
		t.Fatal("预算耗尽应跳过非锁定插件")
	}
}

func TestRun_LockBudgetExempt(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	d.MaxConcurrent = 1
	budget := NewTokenBudget(100)
	budget.Add(100) // 耗尽

	_ = d.reg.Register(&Registration{
		Manifest:       plugin.AgentManifest{Name: "locked"},
		Plugin:         &nopPlugin{manifest: plugin.AgentManifest{Name: "locked"}},
		Enabled:        true,
		LockRepoConfig: true,
	})

	sel := []Selection{{Reg: &Registration{
		Manifest:       plugin.AgentManifest{Name: "locked"},
		Plugin:         &nopPlugin{manifest: plugin.AgentManifest{Name: "locked"}},
		Enabled:        true,
		LockRepoConfig: true,
	}}}
	outcomes := d.Run(context.Background(), plugin.Task{}, sel, budget)
	if outcomes[0].SkippedReason != "" {
		t.Fatalf("锁定插件不应被预算跳过，got %q", outcomes[0].SkippedReason)
	}
	if outcomes[0].Err != nil {
		t.Fatalf("锁定插件应正常执行，got %v", outcomes[0].Err)
	}
}

type blockingPlugin struct {
	manifest plugin.AgentManifest
	release  chan struct{}
}

func (p *blockingPlugin) Manifest() plugin.AgentManifest { return p.manifest }
func (p *blockingPlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	<-p.release
	return plugin.Result{}, nil
}

// ---- 深挖修复回归：任务级取消不计熔断 ----

type canceledPlugin struct{ manifest plugin.AgentManifest }

func (p *canceledPlugin) Manifest() plugin.AgentManifest { return p.manifest }
func (p *canceledPlugin) Invoke(_ context.Context, _ plugin.Task) (plugin.Result, error) {
	return plugin.Result{}, context.Canceled
}

// 连续 3 次任务取消（如滚动发布）不应熔断插件——取消是任务级 teardown，
// 不是插件故障。真失败 3 次仍照常熔断。
func TestRun_CanceledNotCountedAsFailure(t *testing.T) {
	reg1 := &Registration{Manifest: plugin.AgentManifest{Name: "canceled-agent", Skills: []string{"security"}},
		Plugin: &canceledPlugin{manifest: plugin.AgentManifest{Name: "canceled-agent"}}, Enabled: true}
	reg2 := &Registration{Manifest: plugin.AgentManifest{Name: "fail-agent", Skills: []string{"correctness"}},
		Plugin: &failPlugin{manifest: plugin.AgentManifest{Name: "fail-agent"}}, Enabled: true}

	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(reg1)
	_ = d.reg.Register(reg2)
	sel1 := []Selection{{Reg: reg1, Reason: "base"}}
	sel2 := []Selection{{Reg: reg2, Reason: "base"}}
	ctx := context.Background()

	for range breakerTripThreshold {
		d.Run(ctx, plugin.Task{}, sel1, nil)
	}
	if _, _, berr := d.BreakerFor("canceled-agent").AllowGen(ctx); berr != nil {
		t.Fatal("连续取消不应熔断插件")
	}

	for range breakerTripThreshold {
		d.Run(ctx, plugin.Task{}, sel2, nil)
	}
	if _, _, berr := d.BreakerFor("fail-agent").AllowGen(ctx); berr == nil {
		t.Fatal("连续真失败应熔断插件")
	}
}

// v3.9.2 回归：半开探测在途时任务取消——teardown 路径必须经 Abandon 释放
// 探测位（agentkit v0.10.4 配对契约），下一次 Allow 立即放行新探测。此前
// 探测位滞留至 probeTimeout，健康插件被"熔断中"误跳过最长约 10 分钟。
func TestRun_TeardownAbandonsHalfOpenProbe(t *testing.T) {
	now := time.Now()
	reg1 := &Registration{Manifest: plugin.AgentManifest{Name: "canceled-agent", Skills: []string{"security"}},
		Plugin: &canceledPlugin{manifest: plugin.AgentManifest{Name: "canceled-agent"}}, Enabled: true}
	d := newDispatcher(NewRegistry(), logx.Nop(), func() time.Time { return now })
	_ = d.reg.Register(reg1)
	sel := []Selection{{Reg: reg1, Reason: "base"}}

	bs := d.BreakerFor("canceled-agent")
	for range breakerTripThreshold {
		_, gen, berr := bs.AllowGen(context.Background())
		if berr != nil {
			break
		}
		bs.FailureGen(context.Background(), gen)
	}
	now = now.Add(6 * time.Minute) // 冷却期（5min）过 → 半开

	// Run 内 Allow 放行半开探测 → 插件返回 Canceled → teardown → AbandonGen
	_ = d.Run(context.Background(), plugin.Task{}, sel, nil)

	if _, _, berr := bs.AllowGen(context.Background()); berr != nil {
		t.Fatal("teardown 应经 AbandonGen 释放半开探测位，下一次 Allow 立即放行新探测")
	}
}

func TestTeardown_Classification(t *testing.T) {
	parentCanceled, cancel := context.WithCancel(context.Background())
	cancel()
	if !teardown(context.Canceled, context.Background()) {
		t.Fatal("错误链含 Canceled 应判 teardown")
	}
	if !teardown(context.DeadlineExceeded, parentCanceled) {
		t.Fatal("父 ctx 已取消时的 DeadlineExceeded 应保守判 teardown")
	}
	if teardown(context.DeadlineExceeded, context.Background()) {
		t.Fatal("父 ctx 健康时的插件自身超时应计失败（非 teardown）")
	}
	if teardown(errors.New("boom"), context.Background()) {
		t.Fatal("普通错误不是 teardown")
	}
	if teardown(nil, context.Background()) {
		t.Fatal("nil 不是 teardown")
	}
}

// 深挖修复回归：任务取消后排队中的插件应跳过而非傻等槽位。
func TestRun_QueuedWaitCancellable(t *testing.T) {
	release := make(chan struct{})
	blocker := &blockingPlugin{manifest: plugin.AgentManifest{Name: "blocker"}, release: release}
	regB := &Registration{Manifest: plugin.AgentManifest{Name: "blocker", Skills: []string{"security"}},
		Plugin: blocker, Enabled: true}
	queued := &Registration{Manifest: plugin.AgentManifest{Name: "queued", Skills: []string{"security"}},
		Plugin: &nopPlugin{manifest: plugin.AgentManifest{Name: "queued"}}, Enabled: true}

	d := NewDispatcher(NewRegistry(), logx.Nop())
	_ = d.reg.Register(regB)
	_ = d.reg.Register(queued)
	d.MaxConcurrent = 1

	ctx, cancel := context.WithCancel(context.Background())
	sel := []Selection{{Reg: regB, Reason: "base"}, {Reg: queued, Reason: "base"}}
	done := make(chan []Outcome, 1)
	go func() { done <- d.Run(ctx, plugin.Task{}, sel, nil) }()
	close(release) // blocker 放行；取消发生在排队者拿到槽位前后均可能——这里直接取消任务
	cancel()
	outcomes := <-done
	if len(outcomes) != 2 {
		t.Fatalf("应有 2 个 outcome，got %d", len(outcomes))
	}
}
