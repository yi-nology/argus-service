// delta_test.go —— QA 流式增量发布面：跨实例 fanout（默认，#67）/ live-only 回滚
// / nil 安全 / reasoning 相位出口授权。
package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	oagentprogress "github.com/oagentkit/runtime/progress"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// publishQADelta（fanout 开，nil Cfg=缺省）：TaskID 空、PR 身份齐、载荷对齐；
// Store=nil 证明不 panic（outbox 写在 publishProgress 内 nil-safe 跳过）。
func TestPublishQADeltaLocalBus(t *testing.T) {
	bus := oagentprogress.NewBus[progress.Event]()
	s := &Service{Bus: bus} // Store nil：任何 outbox 写入都会 panic
	ch, cancel := bus.Subscribe(context.Background())
	defer cancel()

	s.publishQADeltaTenant(context.Background(), "github", "o", "r", "7", plugin.AgentDelta{
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
	(&Service{}).publishQADeltaTenant(context.Background(), "github", "o", "r", "1", plugin.AgentDelta{Phase: "reset", Final: true})
}

// TestDeltaFanoutCrossInstance #67 验收（双副本）：任务/QA 在实例 A 发布 delta
// （fanout 默认开 → Outbox），连接实例 B 的 SSE 订阅者经 fanout 回灌收到同一
// 事件（Event.ID 相同）；回滚开关关闭时 B 收不到（live-only）。
func TestDeltaFanoutCrossInstance(t *testing.T) {
	dsn := "file://" + filepath.Join(t.TempDir(), "delta-fanout.db")
	storeA, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := storage.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storeB.Close() })

	busA, busB := oagentprogress.NewBus[progress.Event](), oagentprogress.NewBus[progress.Event]()
	svcA := &Service{Bus: busA, Store: storeA, Cfg: &config.Config{DeltaFanout: true}, Log: testLogger{}}
	svcB := &Service{Bus: busB, Store: storeB, Cfg: &config.Config{DeltaFanout: true}, Log: testLogger{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svcA.StartOutboxFanout(ctx)
	svcB.StartOutboxFanout(ctx)
	chB, cancelB := busB.Subscribe(ctx)
	defer cancelB()

	// A 发布 QA delta（reasoning 相位，覆盖 delta 字段跨实例保真）。
	svcA.publishQADeltaTenant(context.Background(), "gitea", "o", "r", "7", plugin.AgentDelta{
		Phase: "reasoning", Iteration: 2, Seq: 5, Text: "增量片段", Final: false,
	})
	select {
	case ev := <-chB:
		if ev.Type != progress.EventAgentDelta || ev.AgentPhase != "reasoning" ||
			ev.Delta != "增量片段" || ev.Seq != 5 || ev.AgentIteration != 2 || ev.ID == "" {
			t.Fatalf("跨实例 delta 载荷失真: %+v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("跨实例 delta 3s 未送达（fanout 失效）")
	}

	// 回滚开关：DeltaFanout=false 恢复 live-only——B 不再收到。
	svcOff := &Service{Bus: busA, Store: storeA, Cfg: &config.Config{DeltaFanout: false}, Log: testLogger{}}
	svcOff.publishQADeltaTenant(context.Background(), "gitea", "o", "r", "7", plugin.AgentDelta{Phase: "text", Text: "offline"})
	time.Sleep(1200 * time.Millisecond)
	select {
	case ev := <-chB:
		t.Fatalf("回滚后 delta 不应跨实例: %+v", ev)
	default:
	}
}

// TestAuthorizeDeltaPhase reasoning 相位出口授权矩阵（#67）：
// 缺省 admin 面——admin 放行、read/ci 过滤；all 全放；off 全滤；未知相位不过门。
func TestAuthorizeDeltaPhase(t *testing.T) {
	svc := func(mode string) *Service {
		return &Service{Cfg: &config.Config{DeltaReasoning: mode}}
	}
	cases := []struct {
		mode  string
		scope string
		want  bool
	}{
		{"admin", "admin", true},
		{"admin", "read", false},
		{"admin", "ci", false},
		{"admin", "", false}, // 无 scope（未鉴权路径）fail-closed
		{"all", "read", true},
		{"all", "", true},
		{"off", "admin", false},
		{"off", "read", false},
	}
	for _, tc := range cases {
		if got := svc(tc.mode).AuthorizeDeltaPhase(ContextWithScope(context.Background(), tc.scope), "reasoning"); got != tc.want {
			t.Errorf("mode=%s scope=%q = %v, want %v", tc.mode, tc.scope, got, tc.want)
		}
	}
	// 非 reasoning 相位不过门（任何 scope）。
	for _, mode := range []string{"admin", "off"} {
		for _, phase := range []string{"text", "reset", ""} {
			if !svc(mode).AuthorizeDeltaPhase(context.Background(), phase) {
				t.Errorf("mode=%s phase=%q 不应被过滤", mode, phase)
			}
		}
	}
	// nil Cfg / nil Service：缺省 admin 面 + nil 安全。
	if svc("").AuthorizeDeltaPhase(context.Background(), "reasoning") {
		t.Error("缺省面 read scope 应被过滤")
	}
	if !(*Service)(nil).AuthorizeDeltaPhase(context.Background(), "text") {
		t.Error("nil Service 安全（非 reasoning 相位放行）")
	}
}
