package dispatch

import (
	"testing"

	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/plugin"
)

// ---- PruneByBudget tests ----

func TestPruneByBudget_NilBudget(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	sel := []Selection{{Reg: reg("a", nil)}}
	keep, pruned := d.PruneByBudget(sel, nil)
	if len(keep) != 1 || len(pruned) != 0 {
		t.Fatal("nil budget 应不裁剪")
	}
}

func TestPruneByBudget_HeavyPrunedFirst(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	// remaining=50 < reserve=100(10%)：循环直到所有非锁定插件被裁
	budget := NewTokenBudget(1000)
	budget.Add(950) // 剩余 50，低于 10% reserve (100)

	sel := []Selection{
		{Reg: reg("heavy", nil, withCost(plugin.CostHeavy))},
		{Reg: reg("standard", nil, withCost(plugin.CostStandard))},
	}
	keep, pruned := d.PruneByBudget(sel, budget)
	// heavy 先裁（costRank 更高），然后 standard 也被裁（remaining 仍 < reserve）
	if len(pruned) != 2 || pruned[0] != "heavy" || pruned[1] != "standard" {
		t.Fatalf("应先裁 heavy 再裁 standard，pruned=%v", pruned)
	}
	if len(keep) != 0 {
		t.Fatalf("应全部裁完，keep=%v", selNames(keep))
	}
}

func TestPruneByBudget_LockExempt(t *testing.T) {
	d := NewDispatcher(NewRegistry(), logx.Nop())
	budget := NewTokenBudget(1000)
	budget.Add(999) // 剩余 1，几乎耗尽

	sel := []Selection{
		{Reg: reg("locked-heavy", nil, withCost(plugin.CostHeavy), func(r *Registration) { r.LockRepoConfig = true })},
		{Reg: reg("normal", nil, withCost(plugin.CostStandard))},
	}
	keep, pruned := d.PruneByBudget(sel, budget)
	// locked 应豁免，normal 被裁
	if len(pruned) != 1 || pruned[0] != "normal" {
		t.Fatalf("应裁 normal 而非 locked，pruned=%v", pruned)
	}
	if len(keep) != 1 || keep[0].Reg.Manifest.Name != "locked-heavy" {
		t.Fatalf("应保留 locked-heavy，keep=%v", selNames(keep))
	}
}
