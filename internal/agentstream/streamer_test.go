package agentstream

import (
	"strings"
	"testing"
)

// collect 构造 Streamer + 事件收集器。
func collect() (*Streamer, *[]Delta) {
	var got []Delta
	return New(func(d Delta) { got = append(got, d) }), &got
}

// 小分片按窗口合并；缓冲达到 200B 立即发布；seq 同 Phase 单调。
func TestSmallChunksCoalesceThenPublishOnBytes(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseText, "hello")
	if len(*got) != 0 {
		t.Fatalf("小分片不应发布: %+v", *got)
	}

	big := strings.Repeat("x", 250)
	s.OnDelta(PhaseText, big)
	if len(*got) != 1 {
		t.Fatalf("超 200B 应立即发布: %+v", *got)
	}
	if d := (*got)[0]; d.Phase != "text" || d.Seq != 1 || d.Final || d.Text != "hello"+big {
		t.Fatalf("首发增量不符: %+v", d)
	}

	s.OnDelta(PhaseText, strings.Repeat("y", 220))
	if len(*got) != 2 || (*got)[1].Seq != 2 {
		t.Fatalf("seq 应单调递增: %+v", *got)
	}
	s.Close()
	if len(*got) != 2 {
		t.Fatalf("空缓冲收口不应再发: %+v", *got)
	}
}

// 权威快照：Absorb 增量缓冲后整段发布（Seq=0，Final=true）。
func TestSnapshotAbsorbsAndEmitsFinal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase string
	}{
		{"reasoning", PhaseReasoning},
		{"text", PhaseText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, got := collect()
			s.OnDelta(tc.phase, "partial")
			s.OnSnapshot(tc.phase, "full snapshot")
			if len(*got) != 1 {
				t.Fatalf("快照应只发一个 Final: %+v", *got)
			}
			if d := (*got)[0]; d.Phase != tc.phase || d.Seq != 0 ||
				d.Text != "full snapshot" || !d.Final || d.Iteration != 1 {
				t.Fatalf("快照增量不符: %+v", d)
			}
			s.Close()
			if len(*got) != 1 {
				t.Fatalf("快照已吸收缓冲，收口不应再发: %+v", *got)
			}
		})
	}
}

// tool_call 丢弃调用轮前导正文；reasoning 残尾仍由 Close 落地。
func TestToolCallDropsLeadingText(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseReasoning, "think")
	s.OnDelta(PhaseText, "调用前正文")
	s.OnToolCall()
	if len(*got) != 0 {
		t.Fatalf("tool_call 不应直接发事件: %+v", *got)
	}
	s.Close()
	if len(*got) != 1 {
		t.Fatalf("应只剩 reasoning 残尾: %+v", *got)
	}
	if d := (*got)[0]; d.Phase != "reasoning" || d.Text != "think" || !d.Final || d.Seq != 1 || d.Iteration != 1 {
		t.Fatalf("reasoning 残尾不符: %+v", d)
	}
}

// tool_call 只封本轮流：下一条推理/正文事件进入下一轮，增量继续发布
// （固定 iteration=1 的历史缺陷：Absorb 不可逆，只有第一轮有增量）。
func TestNextRoundStreamsAfterToolCall(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseReasoning, "r1")
	s.OnSnapshot(PhaseReasoning, "R1") // 轮 1 封口
	s.OnToolCall()
	// 同一条消息的第二个 tool_call 不得把轮次推进两次。
	s.OnToolCall()
	s.OnDelta(PhaseReasoning, strings.Repeat("z", 250))
	s.OnSnapshot(PhaseReasoning, "R2")
	s.Close()

	if len(*got) != 3 {
		t.Fatalf("应发 R1 快照 + 轮2增量 + R2 快照，got=%+v", *got)
	}
	if d := (*got)[0]; d.Phase != "reasoning" || d.Text != "R1" || d.Iteration != 1 || !d.Final {
		t.Fatalf("轮1快照不符: %+v", d)
	}
	if d := (*got)[1]; d.Phase != "reasoning" || d.Iteration != 2 || d.Seq != 1 || d.Final {
		t.Fatalf("轮2增量应可发布: %+v", d)
	}
	if d := (*got)[2]; d.Phase != "reasoning" || d.Text != "R2" || d.Iteration != 2 || !d.Final {
		t.Fatalf("轮2快照不符: %+v", d)
	}
}

// OnReset：两路缓冲作废并发布 reset（消费方清空累积）。
func TestOnResetEmitsReset(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseReasoning, "r")
	s.OnDelta(PhaseText, "t")
	s.OnReset()
	if len(*got) != 1 {
		t.Fatalf("reset 应只发 reset: %+v", *got)
	}
	if d := (*got)[0]; d.Phase != "reset" || !d.Final || d.Text != "" || d.Seq != 0 {
		t.Fatalf("reset 事件不符: %+v", d)
	}
	s.Close()
	if len(*got) != 1 {
		t.Fatalf("reset 已作废缓冲，收口不应再发: %+v", *got)
	}
}

// OnReset 后必须换新 Pacer：旧轮的 stream 已被快照 Absorb 封口，
// 不换会让重试轮增量全丢（迭代号同时复位为 1）。
func TestOnResetResetsIterationAndPacer(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseReasoning, "r1")
	s.OnSnapshot(PhaseReasoning, "R1")
	s.OnToolCall()
	s.OnReset()
	s.OnDelta(PhaseReasoning, strings.Repeat("z", 250))
	s.OnSnapshot(PhaseReasoning, "R2")
	s.Close()

	if len(*got) != 4 {
		t.Fatalf("应发 R1快照 + reset + 重试轮增量 + R2快照，got=%+v", *got)
	}
	if d := (*got)[1]; d.Phase != "reset" {
		t.Fatalf("第二事件应为 reset: %+v", d)
	}
	if d := (*got)[2]; d.Phase != "reasoning" || d.Iteration != 1 || d.Seq != 1 || d.Final {
		t.Fatalf("重试轮增量应重新从轮1/seq1 开始: %+v", d)
	}
}

// Close 冲刷残尾为 Final=true（调用轮正文没有权威快照）。
func TestCloseFlushesResidualAsFinal(t *testing.T) {
	s, got := collect()
	s.OnDelta(PhaseText, "tail")
	s.Close()
	if len(*got) != 1 {
		t.Fatalf("Close 应冲刷残尾: %+v", *got)
	}
	if d := (*got)[0]; d.Phase != "text" || d.Text != "tail" || !d.Final || d.Seq != 1 {
		t.Fatalf("残尾事件不符: %+v", d)
	}
}

// emit 为 nil 时所有路径都不 panic。
func TestNilEmitSafe(t *testing.T) {
	s := New(nil)
	s.OnDelta(PhaseReasoning, "r")
	s.OnSnapshot(PhaseText, "final")
	s.OnReset()
	s.Close()
}
