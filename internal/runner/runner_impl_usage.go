// runner_impl_usage.go —— Runner 预算闸与用量记账的包级实现。

package runner

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/oagentkit/llm"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
)

// budgetGate 的包级实现（转发壳见原文件）。
func budgetGate(r *Runner, ctx context.Context, task *storage.Task, knobs taskKnobs, realBudget int) (bool, int64) {
	if knobs.monthlyBudget <= 0 && realBudget <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	now := time.Now()
	res, err := r.Store.ReserveRepoBudget(ctx, storage.ReserveInput{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		Platform: task.Platform, Owner: task.Owner, Repo: task.Repo,
		TaskBudget:  knobs.taskTokenBudget,
		QuotaBudget: knobs.monthlyBudget, RealBudget: realBudget,
		Since: time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()),
	})
	if err != nil {
		r.Log.Warn("argus.runner.budget_reserve_failed", "task", task.ID, "error", err.Error())
		// Phase1①：hard 模式判定不可得 fail-closed——账本不可用就不开新计费
		// 调用；soft 保持历史 fail-open（可用性优先）。
		if r.Cfg != nil && r.Cfg.BudgetEnforcement == "hard" {
			r.rejectByBudget(ctx, task,
				// spec §2.1.3：reason 携带机器可读 token budget_ledger_unavailable
				//（runbook/告警可 grep）；中文文案与 markerTail 保持不变。
				"预算账本不可用（预留判定失败），ARGUS_BUDGET_ENFORCEMENT=hard 拒绝放行 [budget_ledger_unavailable]",
				"检查数据库与 leader_leases 后 push 新提交重试；可用性优先可切回 soft。")
			return true, 0
		}
		return false, int64(knobs.taskTokenBudget)
	}
	switch res.Verdict {
	case storage.ReserveQuotaExceeded:
		obs.IncrBudgetExceeded("quota")
		r.Log.Warn("argus.runner.repo_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.QuotaUsed, "budget", knobs.monthlyBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月 token 预算已耗尽（used=%d, budget=%d）", res.QuotaUsed, knobs.monthlyBudget),
			"下月预算重置后 push 新提交可重新触发审查。")
		return true, 0
	case storage.ReserveRealExceeded:
		obs.IncrBudgetExceeded("real")
		r.Log.Warn("argus.runner.real_budget_exceeded", "task", task.ID,
			"repo", task.Owner+"/"+task.Repo, "used", res.RealUsed, "budget", realBudget)
		r.rejectByBudget(ctx, task,
			fmt.Sprintf("仓库本月真实 token 实销+预留已达上限（used=%d, budget=%d）", res.RealUsed, realBudget),
			"下月重置后 push 新提交可重新触发审查；如需立即恢复请调整 ARGUS_REAL_TOKEN_BUDGET。")
		return true, 0
	}
	// 防御兜底：判定通过时授予值应 >0（任务预算非正的无界边界回落原值）。
	if res.GrantedTokens <= 0 {
		return false, int64(knobs.taskTokenBudget)
	}
	return false, res.GrantedTokens
}

// rejectByBudget 的包级实现（转发壳见原文件）。
func rejectByBudget(r *Runner, ctx context.Context, task *storage.Task, reason, markerTail string) {
	if fenced, ferr := r.Store.FinishTaskFenced(ctx, task.ID, storage.StatusFailed, "", "", "", reason, 0, 0); ferr != nil {
		r.Log.Warn("argus.runner.finish_failed", "task", task.ID, "error", ferr.Error())
	} else if fenced {
		r.Log.Warn("argus.runner.finish_fenced", "task", task.ID,
			"hint", "任务归属已易主（stale 重置），本次终态写入丢弃")
	}
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"❌ Argus 审查失败：%s——%s\n\n<!-- argus:start task_id=%s -->",
		reason, markerTail, task.ID))
	r.announceTerminal(ctx, task, storage.StatusFailed, "", nil, reason)
	plugin.ReportTaskTotal(task.Platform, task.Kind, storage.StatusFailed)
}

// recordUsageEvent 的包级实现（转发壳见原文件）。
func recordUsageEvent(r *Runner, ctx context.Context, task *storage.Task, rec llm.UsageRecord) {
	if r.Store == nil || task == nil {
		return
	}
	// 独立超时上下文：不随任务取消（账已发生），停机路径 5s 上界。
	ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	inv := "u-" + uuid.NewString()
	if rec.Iteration > 0 {
		if nonce := execNonceFrom(ctx); nonce != "" {
			inv = fmt.Sprintf("u-%s-%d-%s-%d", task.ID, task.Attempt, nonce, rec.Iteration)
		} else {
			// 无 nonce（非 execContext 装配的直接调用）：保持旧格式向后兼容。
			inv = fmt.Sprintf("u-%s-%d-%d", task.ID, task.Attempt, rec.Iteration)
		}
	}
	ev := &storage.UsageEvent{
		TaskID: task.ID, AttemptID: strconv.Itoa(task.Attempt),
		InvocationID: inv,
		Platform:     task.Platform, Owner: task.Owner, Repo: task.Repo,
		Provider:      r.usageProvider(),
		Model:         rec.Model,
		InputTokens:   rec.PromptTokens,
		OutputTokens:  rec.CompletionTokens,
		CachedTokens:  rec.CachedTokens,
		EstimatedCost: r.estimatedUsageCost(rec),
		Status:        storage.UsageSettled,
		OccurredAt:    time.Now(),
	}
	if err := r.Store.RecordUsageEvent(ictx, ev); err != nil && r.Log != nil {
		// logredact：模型名/URL 类字段无凭据面，直接结构化告警。
		r.Log.Warn("argus.runner.usage_record_failed", "task", task.ID, "error", err.Error())
	}
}

// usageProvider 的包级实现（转发壳见原文件）。
func usageProvider(r *Runner) string {
	if r.Cfg != nil && r.Cfg.LLM.BaseURL != "" {
		if u, err := url.Parse(r.Cfg.LLM.BaseURL); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return "openai"
}

// estimatedUsageCost 的包级实现（转发壳见原文件）。
func estimatedUsageCost(r *Runner, rec llm.UsageRecord) float64 {
	rate := r.costRate(rec.Model)
	if rate[0] <= 0 && rate[1] <= 0 {
		return 0
	}
	ratio := 0.1
	if r.Cfg != nil {
		ratio = r.Cfg.CacheReadRatio()
	}
	inputPerM := cacheAdjustedInput(rate[0]*1000, rec.PromptTokens, rec.CachedTokens, ratio)
	outputPerM := rate[1] * 1000
	return (inputPerM*float64(rec.PromptTokens) + outputPerM*float64(rec.CompletionTokens)) / 1e6
}
