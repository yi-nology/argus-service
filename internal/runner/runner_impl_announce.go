// runner_impl_announce.go —— Runner 进度事件与终态播报的包级实现。

package runner

import (
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
		Type: typ, TaskID: task.ID, Platform: task.Platform,
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
		Type: progress.EventTaskStage, TaskID: task.ID, Platform: task.Platform,
		Owner: task.Owner, Repo: task.Repo, Number: task.Number,
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped("outbox_failed")
		r.Log.Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
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
