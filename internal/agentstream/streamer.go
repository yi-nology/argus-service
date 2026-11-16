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

// Delta 一次 agent 增量（plugin.AgentDelta 的底层类型）。
type Delta struct {
	Phase string // reasoning | text | reset
	// Iteration 环节内轮次（从 1 起）。tool_call 之后的下一个增量/快照进入下一轮。
	// kernel delta.Pacer 的流按 (Phase, Iteration) 键控——同轮快照吸收只封本轮流，
	// 不封后续轮（Absorb 不可逆，固定 iteration=1 会让只有第一轮有增量）。
	Iteration int
	Seq       int    // >0 增量序号（同 Phase 同轮内单调）；0 = 权威快照
	Text      string // 增量合并片段 / 快照全文（reset 时为空）
	Final     bool   // 权威快照或收口残尾
}

// Streamer 单环节增量收敛器：喂入 agentrun 事件，按 kernel delta.Pacer 节流后
// 回调宿主。单 goroutine 契约（OnEvent 在 drain 循环内同步回调），不加锁。
type Streamer struct {
	p    *delta.Pacer
	emit func(Delta)
	// iteration 当前轮次；pendingAdvance 在 tool_call 后置位——下一次推理/正文
	// 事件才真正递增（一条 assistant 消息可能有多个 tool_call，不能每个都加）。
	iteration      int
	pendingAdvance bool
}

// New 构造收敛器；emit 为 nil 时静默（不 panic）。
func New(emit func(Delta)) *Streamer {
	return &Streamer{p: delta.New(), emit: emit, iteration: 1}
}

// Handle 消费一个 agentrun 事件（调用方保证非零值；单 goroutine）。
func (s *Streamer) Handle(ev agentrun.Event) {
	switch ev.Type {
	case agentrun.EventReasoningDelta:
		s.offer(phaseReasoning, ev.Text)
	case agentrun.EventTextDelta:
		s.offer(phaseText, ev.Text)
	case agentrun.EventToolCall:
		// 调用轮前导正文不是终稿：丢弃 text 缓冲（不 emit）。同一条消息可能
		// 有多个 tool_call，轮次递增延后到下一条推理/正文事件（pendingAdvance）。
		s.p.Absorb(phaseText, s.iteration)
		s.pendingAdvance = true
	case agentrun.EventReasoning:
		s.snapshot(phaseReasoning, ev.Text)
	case agentrun.EventText:
		s.snapshot(phaseText, ev.Text)
	case agentrun.EventAttemptRetry:
		// 首跑失败整程重来：旧 Pacer 全部缓冲作废（含已 Absorb 封口的旧轮），
		// 换新 Pacer 并把轮次复位为 1——否则重试轮的 stream 已被封口，增量全丢。
		s.p = delta.New()
		s.iteration = 1
		s.pendingAdvance = false
		s.send(Delta{Phase: phaseReset, Final: true})
	}
}

// Close 环节收口：冲刷全部残尾（调用轮正文无权威快照，只能在此落地）。
func (s *Streamer) Close() {
	if s == nil || s.p == nil {
		return
	}
	for _, pub := range s.p.FlushAll() {
		s.send(Delta{Phase: pub.Kind, Iteration: pub.Iteration, Seq: pub.Seq, Text: pub.Text, Final: true})
	}
}

// nextIteration 取本次推理/正文事件所属轮次：tool_call 之后的首个此类事件
// 进入下一轮（一条消息多个 tool_call 只前进一次）。
func (s *Streamer) nextIteration() int {
	if s.pendingAdvance {
		s.iteration++
		s.pendingAdvance = false
	}
	return s.iteration
}

// offer 增量分片进节流器；发布才 emit（Final=false，消费方按序追加）。
func (s *Streamer) offer(phase, chunk string) {
	it := s.nextIteration()
	if pub, ok := s.p.Offer(phase, it, chunk); ok {
		s.send(Delta{Phase: phase, Iteration: it, Seq: pub.Seq, Text: pub.Text})
	}
}

// snapshot 权威快照：吸收**本轮**增量缓冲后整段下发（Seq=0，Final=true），
// 消费方以快照替换该 Phase 本轮增量累积。只封本轮流，后续轮用新键。
func (s *Streamer) snapshot(phase, text string) {
	it := s.nextIteration()
	s.p.Absorb(phase, it)
	s.send(Delta{Phase: phase, Iteration: it, Seq: 0, Text: text, Final: true})
}

// send emit 回调（nil 安全）。
func (s *Streamer) send(d Delta) {
	if s.emit != nil {
		s.emit(d)
	}
}
