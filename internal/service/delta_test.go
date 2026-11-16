// delta_test.go —— QA 流式增量发布：live-only（本地 Bus），不落 outbox。
package service

import (
	"context"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"

	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
)

// publishQADelta：TaskID 空、PR 身份齐、载荷对齐；Store=nil 证明不触 outbox。
func TestPublishQADeltaLocalBusOnly(t *testing.T) {
	bus := oagentprogress.NewBus[progress.Event]()
	s := &Service{Bus: bus} // Store nil：任何 outbox 写入都会 panic
	ch, cancel := bus.Subscribe(context.Background())
	defer cancel()

	s.publishQADelta("github", "o", "r", "7", plugin.AgentDelta{
		Phase: "reasoning", Seq: 2, Text: "think", Final: false,
	})

	select {
	case ev := <-ch:
		if ev.Type != progress.EventAgentDelta || ev.TaskID != "" {
			t.Fatalf("QA delta 类型/任务身份不符: %+v", ev)
		}
		if ev.Platform != "github" || ev.Owner != "o" || ev.Repo != "r" || ev.Number != "7" {
			t.Fatalf("QA delta PR 身份不符: %+v", ev)
		}
		if ev.AgentPhase != "reasoning" || ev.Delta != "think" || ev.Seq != 2 || ev.Final {
			t.Fatalf("QA delta 载荷不符: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("订阅者未收到 QA agent.delta")
	}
}

// nil Bus 安全。
func TestPublishQADeltaNilBus(t *testing.T) {
	(&Service{}).publishQADelta("github", "o", "r", "1", plugin.AgentDelta{Phase: "reset", Final: true})
}
