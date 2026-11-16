package agentstream

import (
	"github.com/oagentkit/agent/agentrun"
	"github.com/yi-nology/goagent-kernel/delta"
)

// 流相位词表（SSE agent_phase 值）。
const (
	phaseReasoning = "reasoning"
	phaseText      = "text"
	phaseReset     = "reset"
)

// iteration 本包的轮次键固定为 1：Pacer 生命周期是单个环节（一次 agentrun.Run），
// 环节内不再细分为多轮增量流；attempt_retry 用 reset 通知消费方清空累积。
const iteration = 1

// Delta 一次 agent 增量（plugin.AgentDelta 的底层类型）。
type Delta struct {
	Phase string // reasoning | text | reset
	Seq   int    // >0 增量序号（同一 Phase 内单调）；0 = 权威快照
	Text  string // 增量合并片段 / 快照全文（reset 时为空）
	Final bool   // 权威快照或收口残尾
}

// Streamer 单环节增量收敛器：喂入 agentrun 事件，按 kernel delta.Pacer 节流后
// 回调宿主。单 goroutine 契约（OnEvent 在 drain 循环内同步回调），不加锁。
type Streamer struct {
	p    *delta.Pacer
	emit func(Delta)
}

// New 构造收敛器；emit 为 nil 时静默（不 panic）。
func New(emit func(Delta)) *Streamer {
	return &Streamer{p: delta.New(), emit: emit}
}

// Handle 消费一个 agentrun 事件（调用方保证非零值；单 goroutine）。
func (s *Streamer) Handle(ev agentrun.Event) {
	switch ev.Type {
	case agentrun.EventReasoningDelta:
		s.offer(phaseReasoning, ev.Text)
	case agentrun.EventTextDelta:
		s.offer(phaseText, ev.Text)
	case agentrun.EventToolCall:
		// 调用轮前导正文不是终稿：丢弃 text 缓冲（不 emit）。reasoning 逐轮有
		// 权威快照，不在此吸收。
		s.p.Absorb(phaseText, iteration)
	case agentrun.EventReasoning:
		s.snapshot(phaseReasoning, ev.Text)
	case agentrun.EventText:
		s.snapshot(phaseText, ev.Text)
	case agentrun.EventAttemptRetry:
		// 首跑失败整程重来：两路增量缓冲作废，通知消费方清空累积——否则时间线
		// 上两段近似重复无法解释。后续权威快照仍以 Final 落地。
		s.p.Absorb(phaseReasoning, iteration)
		s.p.Absorb(phaseText, iteration)
		s.send(Delta{Phase: phaseReset, Final: true})
	}
}

// Close 环节收口：冲刷全部残尾（调用轮正文无权威快照，只能在此落地）。
func (s *Streamer) Close() {
	if s == nil || s.p == nil {
		return
	}
	for _, pub := range s.p.FlushAll() {
		s.send(Delta{Phase: pub.Kind, Seq: pub.Seq, Text: pub.Text, Final: true})
	}
}

// offer 增量分片进节流器；发布才 emit（Final=false，消费方按序追加）。
func (s *Streamer) offer(phase, chunk string) {
	if pub, ok := s.p.Offer(phase, iteration, chunk); ok {
		s.send(Delta{Phase: phase, Seq: pub.Seq, Text: pub.Text})
	}
}

// snapshot 权威快照：吸收该 Phase 增量缓冲后整段下发（Seq=0，Final=true），
// 消费方以快照替换该 Phase 的增量累积。
func (s *Streamer) snapshot(phase, text string) {
	s.p.Absorb(phase, iteration)
	s.send(Delta{Phase: phase, Seq: 0, Text: text, Final: true})
}

// send emit 回调（nil 安全）。
func (s *Streamer) send(d Delta) {
	if s.emit != nil {
		s.emit(d)
	}
}
