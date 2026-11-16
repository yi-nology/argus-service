package progress

import (
	"encoding/json"
	"strings"
	"testing"
)

// 既有事件对象的 JSON 不含 agent.delta 专用字段（omitempty 保证旧契约逐字不变）。
func TestEventJSONCompatOldEvents(t *testing.T) {
	b, err := json.Marshal(Event{
		Type: EventTaskCreated, TaskID: "t1", Platform: "github",
		Owner: "o", Repo: "r", Number: "7",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"agent_phase"`, `"delta"`, `"seq"`, `"final"`} {
		if strings.Contains(s, key) {
			t.Fatalf("旧事件 JSON 不应出现新字段 %s: %s", key, s)
		}
	}
}

// agent.delta 事件字段可完整往返。
func TestEventAgentDeltaJSONRoundTrip(t *testing.T) {
	in := Event{
		Type: EventAgentDelta, Platform: "github", Owner: "o", Repo: "r", Number: "7",
		AgentPhase: "text", Delta: "hello", Seq: 3, Final: true,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Event
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != EventAgentDelta || out.AgentPhase != "text" || out.Delta != "hello" ||
		out.Seq != 3 || !out.Final {
		t.Fatalf("agent.delta 往返失真: %+v (json=%s)", out, b)
	}
	// reset 语义：payload 字段缺省（seq/final 仍在；delta 省略）
	b, _ = json.Marshal(Event{Type: EventAgentDelta, AgentPhase: "reset", Final: true})
	if strings.Contains(string(b), `"delta"`) || strings.Contains(string(b), `"seq"`) {
		t.Fatalf("reset 事件不应带 delta/seq: %s", b)
	}
}
