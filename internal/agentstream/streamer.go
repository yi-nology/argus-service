package agentstream

import (
	"github.com/oagentkit/agent/agentrun"
	"github.com/yi-nology/goagent-kernel/delta"
)

// 流相位词表（SSE agent_phase 值；导出供宿主直喂 runtime 事件——与
// goagent-runtime chat.DeltaKind 同词表）。
const (
	PhaseReasoning = "reasoning"
	PhaseText      = "text"
	PhaseReset     = "reset"
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

// Streamer 单环节增量收敛器：喂入事件（agentrun 事件经 Handle，或
// goagent-runtime 事件经 OnDelta/OnToolCall/OnReset/OnSnapshot 直喂），
// 按 kernel delta.Pacer 节流后回调宿主。单 goroutine 契约（事件在
// drain 循环内同步回调），不加锁。
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
// agentrun 路径（builtin_react 等存量宿主）；qa 已切 runtime 直喂入口。
func (s *Streamer) Handle(ev agentrun.Event) {
	switch ev.Type {
	case agentrun.EventReasoningDelta:
		s.OnDelta(PhaseReasoning, ev.Text)
	case agentrun.EventTextDelta:
		s.OnDelta(PhaseText, ev.Text)
	case agentrun.EventToolCall:
		s.OnToolCall()
	case agentrun.EventReasoning:
		s.OnSnapshot(PhaseReasoning, ev.Text)
	case agentrun.EventText:
		s.OnSnapshot(PhaseText, ev.Text)
	case agentrun.EventAttemptRetry:
		s.OnReset()
	}
}

// OnDelta 增量分片（goagent-runtime chat.DeltaKind 同词表：text/reasoning）。
// 未知 Kind 归正文——词表开放但 SSE 消费面只认两相，宁可落 text 流也不产
// 无人识别的相位。
func (s *Streamer) OnDelta(phase, text string) {
	if phase != PhaseReasoning {
		phase = PhaseText
	}
	s.offer(phase, text)
}

// OnToolCall 工具调用轮边界：调用轮前导正文不是终稿——丢弃 text 缓冲
// （不 emit）；轮次递增延后到下一条推理/正文事件（pendingAdvance）。
func (s *Streamer) OnToolCall() {
	s.p.Absorb(PhaseText, s.iteration)
	s.pendingAdvance = true
}

// OnReset 整程重来（agentrun AttemptRetry / 宿主回喂重试的等价边界）：
// 旧 Pacer 全部缓冲作废（含已 Absorb 封口的旧轮），轮次复位——否则重试轮
// 的 stream 已被封口，增量全丢。
func (s *Streamer) OnReset() {
	s.p = delta.New()
	s.iteration = 1
	s.pendingAdvance = false
	s.send(Delta{Phase: PhaseReset, Final: true})
}

// OnSnapshot 权威快照：吸收**本轮**增量缓冲后整段下发（Seq=0，Final=true），
// 消费方以快照替换该 Phase 本轮增量累积。只封本轮流，后续轮用新键。
// qa 迁移后 agentloop 不产快照事件（终答只在 Result.Final），由宿主在
// Run 成功后补喂本方法。
func (s *Streamer) OnSnapshot(phase, text string) {
	s.snapshot(phase, text)
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

// snapshot 权威快照（见 OnSnapshot）。
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
