// runner_impl_announce.go —— Runner 进度事件与终态播报的包级实现。

package runner

import (
	"github.com/yi-nology/argus/internal/tenant"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/yi-nology/argus/internal/notify"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// publish 的包级实现（转发壳见原文件）。
func publish(r *Runner, typ string, task *storage.Task, status, verdict string) {
	if r.Bus == nil || task == nil {
		return
	}
	ev := progress.Event{
		Type: typ, TaskID: task.ID, Tenant: task.TenantID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		Status: status, Verdict: verdict,
	}
	r.publishOutbox(ev)
}

// publishStage 的包级实现（转发壳见原文件）。
func publishStage(r *Runner, task *storage.Task, stage string) {
	if r.Bus == nil || task == nil {
		return
	}
	r.publishOutbox(progress.Event{
		Type: progress.EventTaskStage, TaskID: task.ID, Tenant: task.TenantID,
		Platform: task.Platform,
		Owner:    task.Owner, Repo: task.Repo, Number: task.Number,
		Stage: stage,
	})
}

// publishOutbox 的包级实现（转发壳见原文件）。
func publishOutbox(r *Runner, ev progress.Event) {
	ev.ID = progress.NewEventID()
	if r.EventSeen != nil {
		r.EventSeen.Add(ev.ID)
	}
	r.Bus.Publish(ev)
	if r.Store == nil {
		return // 打桩场景无 outbox 面（本地 Bus 已送达）
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = tenant.NewContext(ctx, ev.Tenant) // 事件自带租户（#110：指标按租户归因）
	if err := r.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped(ctx, "outbox_failed")
		r.Log.Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
}

// publishDelta 的包级实现（转发壳见原文件）：
//   - DeltaFanout 开（默认，#67）：与可靠事件同管道——本地 Bus 即时 + Outbox
//     跨实例 fanout 回灌（EventSeen 登记，同实例 at-most-once）。
//   - DeltaFanout 关（ARGUS_DELTA_FANOUT=0 回滚）：旧 live-only——只发本地 Bus，
//     不落 outbox、不登记 EventSeen（事件无 ID，行为与 v7.4.x 逐字一致）。
func publishDelta(r *Runner, task *storage.Task, d plugin.AgentDelta) {
	if r.Bus == nil || task == nil {
		return
	}
	ev := progress.Event{
		Type: progress.EventAgentDelta, TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
		AgentPhase: d.Phase, AgentIteration: d.Iteration,
		Delta: d.Text, Seq: d.Seq, Final: d.Final,
	}
	if r.Cfg != nil && !r.Cfg.DeltaFanout {
		r.Bus.Publish(ev)
		return
	}
	r.publishOutbox(ev)
}

// postStartMarker 的包级实现（转发壳见原文件）。
func postStartMarker(r *Runner, ctx context.Context, task *storage.Task) {
	r.setStartMarker(ctx, task, fmt.Sprintf(
		"🔍 Argus 正在审查中，请稍候…\n\n<!-- argus:start task_id=%s -->",
		task.ID,
	))
}

// setStartMarker 的包级实现（转发壳见原文件）。
func setStartMarker(r *Runner, ctx context.Context, task *storage.Task, body string) {
	if r.SDK == nil {
		return
	}
	if comments, err := r.SDK.ListIssueComments(ctx, task.Platform, task.Owner, task.Repo, task.Number); err == nil {
		for i := len(comments) - 1; i >= 0 && i >= len(comments)-50; i-- {
			c := comments[i]
			if c == nil || !strings.Contains(c.Body, "<!-- argus:start task_id=") {
				continue
			}
			if _, uerr := r.SDK.UpdateIssueComment(ctx, task.Platform, task.Owner, task.Repo, task.Number, c.ID, body); uerr != nil {
				r.Log.Warn("argus.runner.start_marker_update_failed", "task", task.ID, "error", uerr.Error())
			}
			return
		}
	}
	if _, err := r.SDK.CreateIssueComment(ctx, task.Platform, task.Owner, task.Repo, task.Number, body); err != nil {
		r.Log.Warn("argus.runner.start_marker_failed", "task", task.ID, "error", err.Error())
	}
}

// announceTerminal 的包级实现（转发壳见原文件）。
func announceTerminal(r *Runner, ctx context.Context, task *storage.Task, status, verdict string, out *plugin.WorkflowOutput, failReason string) {
	r.publish(progress.EventTaskFinished, task, status, verdict)
	// 审计留痕（v3.86.0）：verdict 终态是审查链路的关键可审计动作
	r.Audit.Log("verdict.posted", "task", task.ID, "platform", task.Platform,
		"owner", task.Owner, "repo", task.Repo, "number", task.Number,
		"status", status, "verdict", verdict, "rule", ruleOf(out))
	if r.Notify == nil {
		return
	}
	ev := notify.TaskEvent{
		TaskID: task.ID, Platform: task.Platform, Owner: task.Owner,
		Repo: task.Repo, Number: task.Number, Status: status, Verdict: verdict,
		FailReason: failReason,
	}
	if out != nil {
		ev.Rule = out.Rule
		ev.Tokens = out.Tokens
		ev.Elapsed = out.Elapsed
		if out.Merged != nil {
			ev.Findings = len(out.Merged.Findings)
		}
	}
	r.Notify.NotifyTerminal(ctx, ev)
}
