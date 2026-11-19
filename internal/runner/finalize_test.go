// finalize_test.go —— 终态漏斗边界：cause→status 单源 + fenced 不公告/不报指标。
package runner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"
	"github.com/oekit/logx"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/worker"
)

// TestTerminalStatus cause→终态单源（done/stale/failed）。
func TestTerminalStatus(t *testing.T) {
	cases := []struct {
		name  string
		cause terminalCause
		want  string
	}{
		{"done", causeDone, storage.StatusDone},
		{"stale", causeStale, storage.StatusStale},
		{"failed", causeFailed, storage.StatusFailed},
		{"unknown-cause-falls-back-to-failed", terminalCause(99), storage.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalStatus(tc.cause); got != tc.want {
				t.Fatalf("terminalStatus(%d) = %q, want %q", tc.cause, got, tc.want)
			}
		})
	}
}

// TestFinishTaskFencedSkipsAnnounceAndMetrics fenced（归属已易主）时只记
// finish_fenced：不写库、不公告（Bus 无事件）、不报指标。
func TestFinishTaskFencedSkipsAnnounceAndMetrics(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "finalize-fenced.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.RunnerID = "runner-a" // 装配归属：pending 任务不满足 status=running → fenced

	bus := oagentprogress.NewBus[progress.Event]()
	r := &Runner{Store: store, Bus: bus, Log: logx.Nop()}
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "7")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	var metrics []string
	oldHook := plugin.HookTaskTotal
	plugin.HookTaskTotal = func(_, platform, kind, status string) {
		metrics = append(metrics, status)
	}
	t.Cleanup(func() { plugin.HookTaskTotal = oldHook })

	ch, cancelSub := bus.Subscribe(context.Background())
	defer cancelSub()

	fenced, ferr := finishTask(r, context.Background(), terminalWrite{
		Task: task, Cause: causeDone, Verdict: "approve", Tokens: 5, Announce: true,
	})
	if ferr != nil {
		t.Fatalf("fenced 写入不应返回错误: %v", ferr)
	}
	if !fenced {
		t.Fatal("归属失配应返回 fenced=true")
	}
	if len(metrics) != 0 {
		t.Fatalf("fenced 不应记任务终态指标: %v", metrics)
	}
	select {
	case ev := <-ch:
		t.Fatalf("fenced 不应公告终态事件: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	row, err := store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusPending {
		t.Fatalf("fenced 写入不应改状态: got %s, want pending", row.Status)
	}
}

// TestFinishTaskWritesAnnouncesAndReports 正常路径：落库 → 公告 → 指标（顺序由
// 漏斗单源保证），调用方通过返回值继续旁路。
func TestFinishTaskWritesAnnouncesAndReports(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "finalize-done.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.RunnerID = "runner-a"

	bus := oagentprogress.NewBus[progress.Event]()
	r := &Runner{Store: store, Bus: bus, Log: logx.Nop()}
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "8")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	q := &worker.StoreQueue{Store: store}
	if _, ok, err := q.ClaimNextPending(context.Background()); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	var metrics []string
	oldHook := plugin.HookTaskTotal
	plugin.HookTaskTotal = func(_, platform, kind, status string) {
		metrics = append(metrics, platform+"/"+kind+"/"+status)
	}
	t.Cleanup(func() { plugin.HookTaskTotal = oldHook })

	ch, cancelSub := bus.Subscribe(context.Background())
	defer cancelSub()

	fenced, ferr := finishTask(r, context.Background(), terminalWrite{
		Task: task, Cause: causeDone, Verdict: "approve", Report: "body",
		ResultJSON: `{}`, Tokens: 12, Announce: true,
	})
	if ferr != nil {
		t.Fatalf("finishTask: %v", ferr)
	}
	if fenced {
		t.Fatal("已 claim 的任务不应被 fenced")
	}

	select {
	case ev := <-ch:
		if ev.Type != progress.EventTaskFinished || ev.Status != storage.StatusDone || ev.Verdict != "approve" {
			t.Fatalf("公告事件不符: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("正常终态应公告 finished 事件")
	}

	if len(metrics) != 1 || metrics[0] != "gitea/code_review/done" {
		t.Fatalf("指标不符: %v", metrics)
	}
	row, err := store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != storage.StatusDone || row.Verdict != "approve" || row.TokensUsed != 12 {
		t.Fatalf("终态落库不符: status=%s verdict=%s tokens=%d", row.Status, row.Verdict, row.TokensUsed)
	}
}

// TestFinishTaskAfterWriteOrder 双钩子顺序：AfterWrite（fenced 判定前）→
// AfterWriteLive（仅未 fenced）→ 公告 → 指标。两个钩子执行时 Bus 均尚无终态
// 事件；指标钩子执行时事件已广播。
func TestFinishTaskAfterWriteOrder(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "finalize-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.RunnerID = "runner-a"

	bus := oagentprogress.NewBus[progress.Event]()
	r := &Runner{Store: store, Bus: bus, Log: logx.Nop()}
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "9")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	q := &worker.StoreQueue{Store: store}
	if _, ok, err := q.ClaimNextPending(context.Background()); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	ch, cancelSub := bus.Subscribe(context.Background())
	defer cancelSub()

	var order []string
	oldHook := plugin.HookTaskTotal
	plugin.HookTaskTotal = func(_, platform, kind, status string) {
		select {
		case ev := <-ch:
			if ev.Type != progress.EventTaskFinished || ev.Status != storage.StatusDone {
				t.Errorf("公告事件不符: %+v", ev)
			}
			order = append(order, "announce")
		default:
			t.Error("指标钩子执行时应已公告终态事件")
		}
		order = append(order, "metric")
	}
	t.Cleanup(func() { plugin.HookTaskTotal = oldHook })

	fenced, ferr := finishTask(r, context.Background(), terminalWrite{
		Task: task, Cause: causeDone, Verdict: "approve", Tokens: 1, Announce: true,
		AfterWrite: func() {
			order = append(order, "after-write")
			select {
			case ev := <-ch:
				t.Errorf("AfterWrite 时公告已发生: %+v", ev)
			default:
			}
		},
		AfterWriteLive: func() {
			order = append(order, "after-live")
			select {
			case ev := <-ch:
				t.Errorf("AfterWriteLive 时公告已发生: %+v", ev)
			default:
			}
		},
	})
	if ferr != nil {
		t.Fatalf("finishTask: %v", ferr)
	}
	if fenced {
		t.Fatal("已 claim 的任务不应被 fenced")
	}
	if got, want := strings.Join(order, ","), "after-write,after-live,announce,metric"; got != want {
		t.Fatalf("顺序 = %q, want %q", got, want)
	}
}

// TestFinishTaskFencedRunsAfterWrite fenced 分支的钩子语义：AfterWrite 在 fenced
// 判定前执行（站点 3/7 的失败 marker 原行为），AfterWriteLive 被短路跳过，
// 且不公告、不报指标。
func TestFinishTaskFencedRunsAfterWrite(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "finalize-fenced-hook.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.RunnerID = "runner-a" // pending 任务不满足 status=running → fenced

	bus := oagentprogress.NewBus[progress.Event]()
	r := &Runner{Store: store, Bus: bus, Log: logx.Nop()}
	task := storage.NewTask(domain.TaskKindCodeReview, "gitea", "o", "r", "10")
	if err := store.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	var metrics int
	oldHook := plugin.HookTaskTotal
	plugin.HookTaskTotal = func(_, platform, kind, status string) { metrics++ }
	t.Cleanup(func() { plugin.HookTaskTotal = oldHook })

	ch, cancelSub := bus.Subscribe(context.Background())
	defer cancelSub()

	ranAfter := false
	liveRan := false
	fenced, ferr := finishTask(r, context.Background(), terminalWrite{
		Task: task, Cause: causeFailed, FailReason: "boom", Announce: true,
		AfterWrite:     func() { ranAfter = true },
		AfterWriteLive: func() { liveRan = true },
	})
	if ferr != nil {
		t.Fatalf("fenced 写入不应返回错误: %v", ferr)
	}
	if !fenced {
		t.Fatal("归属失配应返回 fenced=true")
	}
	if !ranAfter {
		t.Fatal("fenced 短路前应执行 AfterWrite")
	}
	if liveRan {
		t.Fatal("fenced 应跳过 AfterWriteLive")
	}
	if metrics != 0 {
		t.Fatalf("fenced 不应记任务终态指标: %d", metrics)
	}
	select {
	case ev := <-ch:
		t.Fatalf("fenced 不应公告终态事件: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}
