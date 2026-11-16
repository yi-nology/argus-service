// delta_test.go —— P3 流式增量发布面：只走本地 Bus、不落 outbox、nil 安全。
package runner

import (
	"context"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

func deltaTestTask() *storage.Task {
	return &storage.Task{ID: "t1", Platform: "github", Owner: "o", Repo: "r", Number: "7"}
}

// publishDelta 只发本地 Bus，字段对齐 AgentDelta；Store=nil 证明不触 outbox。
func TestPublishDeltaLocalBusOnly(t *testing.T) {
	bus := oagentprogress.NewBus[progress.Event]()
	r := &Runner{Bus: bus} // Store nil：任何 outbox 写入都会 panic
	ch, cancel := bus.Subscribe(context.Background())
	defer cancel()

	r.publishDelta(deltaTestTask(), plugin.AgentDelta{
		Phase: "text", Seq: 3, Text: "hello", Final: false,
	})

	select {
	case ev := <-ch:
		if ev.Type != progress.EventAgentDelta || ev.TaskID != "t1" ||
			ev.Platform != "github" || ev.Owner != "o" || ev.Repo != "r" || ev.Number != "7" {
			t.Fatalf("agent.delta 身份字段不符: %+v", ev)
		}
		if ev.AgentPhase != "text" || ev.Delta != "hello" || ev.Seq != 3 || ev.Final {
			t.Fatalf("agent.delta 载荷不符: %+v", ev)
		}
		if ev.ID != "" {
			t.Fatalf("live-only delta 不应生成 outbox/去重事件 ID: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到 agent.delta")
	}
}

// nil Bus / nil task 不 panic，且不产生事件。
func TestPublishDeltaNilSafe(t *testing.T) {
	r := &Runner{}
	r.publishDelta(deltaTestTask(), plugin.AgentDelta{Phase: "reasoning", Text: "x"})
	r.publishDelta(nil, plugin.AgentDelta{Phase: "text", Text: "x"})

	bus := oagentprogress.NewBus[progress.Event]()
	r.Bus = bus
	ch, cancel := bus.Subscribe(context.Background())
	defer cancel()
	r.publishDelta(nil, plugin.AgentDelta{Phase: "text", Text: "x"})
	select {
	case ev := <-ch:
		t.Fatalf("nil task 不应发布: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// deltaSinkFor 由 Cfg.ReActStream 驱动（nil Cfg 安全）；关闭时 nil = 旧整段语义。
func TestDeltaSinkFor(t *testing.T) {
	task := deltaTestTask()
	r := &Runner{}
	if r.deltaSinkFor(task) != nil {
		t.Fatal("nil Cfg 应返回 nil sink")
	}
	r.Cfg = &config.Config{}
	if r.deltaSinkFor(task) != nil {
		t.Fatal("ReActStream=false 应返回 nil sink")
	}
	r.Cfg.ReActStream = true
	sink := r.deltaSinkFor(task)
	if sink == nil {
		t.Fatal("ReActStream=true 应返回非 nil sink")
	}
	sink(plugin.AgentDelta{Phase: "text", Text: "x"}) // nil Bus 不 panic
}
