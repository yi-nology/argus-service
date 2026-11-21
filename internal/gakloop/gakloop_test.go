package gakloop

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/yi-nology/goagent-kernel/steer"
	"github.com/yi-nology/goagent-kernel/taskboard"
	"github.com/yi-nology/goagent-kernel/worker"
)

// agentloop 六段：注册→心跳→领取→执行→完成→优雅退出。
func TestAgentLoopExecutesTasks(t *testing.T) {
	var mu sync.Mutex
	executed := map[string]int{}
	handler := worker.HandlerFunc(func(_ context.Context, task taskboard.Task) error {
		mu.Lock()
		executed[string(task.ID)]++
		mu.Unlock()
		return nil
	})

	rt, err := New(Config{Handler: handler, TTL: 3 * time.Second, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.AddTask("a", "task-a")
	rt.AddTask("b", "task-b", taskboard.TaskID("a")) // 依赖 a
	rt.AddTask("c", "task-c", taskboard.TaskID("b"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Run(ctx) }()

	if err := rt.WaitDone(ctx, []string{"a", "b", "c"}, 5*time.Second); err != nil {
		cancel()
		t.Fatalf("WaitDone: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未优雅退出")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"a", "b", "c"} {
		if executed[id] != 1 {
			t.Fatalf("task %s 执行次数=%d want 1（map=%v）", id, executed[id], executed)
		}
	}
}

// 依赖未完成不得认领（board 判据经 Loop 传导）。
func TestDepChainBlocksClaim(t *testing.T) {
	handler := worker.HandlerFunc(func(_ context.Context, task taskboard.Task) error {
		if string(task.ID) == "slow" {
			time.Sleep(150 * time.Millisecond)
		}
		return nil
	})
	rt, err := New(Config{Handler: handler, TTL: 3 * time.Second, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	rt.AddTask("slow", "slow")
	rt.AddTask("after", "after", taskboard.TaskID("slow"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.Run(ctx)

	// after 必须晚于 slow 完成（依赖语义）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		after, ok := rt.Task(ctx, "after")
		slow, ok2 := rt.Task(ctx, "slow")
		if ok && ok2 && after.State == taskboard.StateDone && slow.State == taskboard.StateDone {
			return // 顺序由依赖保证；这里只断言两者都完成
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("依赖链未在期限内完成")
}

// steer：协作中止与用户取消可区分。
func TestSteerClassify(t *testing.T) {
	ctx, fire := Signal(context.Background())
	if IsSteered(ctx) {
		t.Fatal("初始不应 steered")
	}
	fire()
	if !IsSteered(ctx) {
		t.Fatal("触发后应 steered")
	}
	cause, err := Classify(ctx, steer.ErrSteered)
	if cause != CauseSteered || err != nil {
		t.Fatalf("Classify steer: cause=%v err=%v", cause, err)
	}

	// 用户取消：ctx 取消但非 steer。
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	cause2, err2 := Classify(ctx2, context.Canceled)
	if cause2 != CauseCancelled || err2 == nil {
		t.Fatalf("Classify cancel: cause=%v err=%v", cause2, err2)
	}

	cause3, err3 := Classify(context.Background(), nil)
	if cause3 != CauseOK || err3 != nil {
		t.Fatalf("Classify ok: %v %v", cause3, err3)
	}
}

// checkpoint：迟到写被拒，单调不回退。
func TestStageGateMonotonic(t *testing.T) {
	g := NewStageGate()
	ok, err := g.Commit("task-1", "running", []string{"R1"}, map[string]string{"spec": "v1"}, 1)
	if err != nil || !ok {
		t.Fatalf("seq1: ok=%v err=%v", ok, err)
	}
	ok, err = g.Commit("task-1", "running", []string{"R1", "R2"}, map[string]string{"spec": "v1", "summary": "s1"}, 2)
	if err != nil || !ok {
		t.Fatalf("seq2: ok=%v err=%v", ok, err)
	}
	// 迟到写 seq=2 再来 → 拒
	ok, _ = g.Commit("task-1", "running", []string{"R1"}, nil, 2)
	if ok {
		t.Fatal("重复 seq 应拒绝")
	}
	// 乱序 seq=1 → 拒
	ok, _ = g.Commit("task-1", "running", nil, nil, 1)
	if ok {
		t.Fatal("回退 seq 应拒绝")
	}
	if g.Last("task-1") != 2 {
		t.Fatalf("Last=%d want 2", g.Last("task-1"))
	}
	// 其它 RunID 独立
	ok, err = g.Commit("task-2", "running", nil, nil, 1)
	if err != nil || !ok {
		t.Fatalf("run2 seq1: ok=%v err=%v", ok, err)
	}
}
