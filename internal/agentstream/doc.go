// Package agentstream 把 agentrun 原生流式事件收敛为宿主可发布的增量（Delta）。
//
// 层级：纯适配层（无 internal 依赖）——上游只认 oagentkit/agent 的事件词表与
// goagent-kernel/delta 的节流契约；宿主经 plugin.AgentDelta 消费。
//
// 边界：节流窗口/字节帽/seq 单调/权威快照吸收全部委托 kernel delta.Pacer（单
// goroutine 回调契约）；本包只做事件词表映射与 reset/flush 语义，不持有第二个
// 真相。Delta 文本来自模型输出，只许进 SSE 观测面，不得落报告/评论/日志。
package agentstream
