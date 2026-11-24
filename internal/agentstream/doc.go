// Package agentstream 把 agent 增量收敛为宿主可发布的流相位（Delta）。
//
// 层级：纯适配层（无 internal 依赖）——上游只认 goagent-kernel/delta 的
// 节流契约；事件入口是 OnDelta/OnToolCall/OnSnapshot/OnReset 直喂（宿主
// 或 loopadapt.NewStreamEmitter 从 agentloop 事件路由），agentrun 事件面
// 已随三条 ReAct 线迁 agentloop 退役。宿主经 plugin.AgentDelta 消费。
//
// 边界：节流窗口/字节帽/seq 单调/权威快照吸收全部委托 kernel delta.Pacer（单
// goroutine 回调契约）；本包只做相位语义与 reset/flush，不持有第二个
// 真相。Delta 文本来自模型输出，只许进 SSE 观测面，不得落报告/评论/日志。
package agentstream
