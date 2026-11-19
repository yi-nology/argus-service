// Package progress Argus 进度事件定义 + oagentkit 泛型总线适配。
// Event 类型是 Argus 领域特有的；Bus 委托给 oagentkit/runtime/progress.Bus[T]。
package progress

import (
	"fmt"
	"time"

	"github.com/oekit/pkg/stringx"
)

// 事件 Type 词表（v3.109.0 常量化——此前 5 处发布点裸字面量，词表只活在
// 注释里；SSE 无类型过滤，新增类型对既有消费者透明）。
const (
	EventTaskCreated  = "task.created"
	EventTaskRunning  = "task.running"
	EventTaskStage    = "task.stage"
	EventTaskFinished = "task.finished"
	// EventAgentDelta ReAct/QA 流式增量（P3 opt-in，ARGUS_REACT_STREAM）：
	// live-only 观测事件，只经本地 Bus 进 SSE，不落 outbox；旧消费方忽略未知
	// 类型即可。字段语义见 Event.AgentPhase/Delta/Seq/Final。
	EventAgentDelta = "agent.delta"
)

// Event 词表：Type ∈ EventTask* 常量。task.stage（v3.40.0）：阶段级进度
// （Stage ∈ R1/R2/R3…），消费者可展示「现在跑到哪一步」。
type Event struct {
	// ID 事件唯一标识（uuid；P1-05 Outbox 跨实例分发与进程内快路径双源去重键：
	// 本实例先 Bus 后落 outbox，他实例的 fanout 环回读同 ID 事件跳过重播）。
	ID     string `json:"id,omitempty"`
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
	// Tenant 事件归属租户（#105；omitempty 保旧 JSON 逐字不变）——SSE 逐事件
	// 过滤的租户维度：local 空坐标任务跨租户不再互见元数据。
	Tenant   string `json:"tenant,omitempty"`
	Platform string `json:"platform,omitempty"`
	Owner    string `json:"owner,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Number   string `json:"number,omitempty"`
	Status   string `json:"status,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Stage    string `json:"stage,omitempty"`
	At       string `json:"at"`

	// agent.delta 专用字段（全部 omitempty：既有事件 JSON 逐字不变）。
	AgentPhase     string `json:"agent_phase,omitempty"`     // reasoning|text|reset
	AgentIteration int    `json:"agent_iteration,omitempty"` // 环节内轮次（从 1 起）
	Delta          string `json:"delta,omitempty"`           // 增量合并片段 / 快照全文（reset 空）
	Seq            int    `json:"seq,omitempty"`             // >0 同 Phase 同轮序号；0 = 权威快照
	Final          bool   `json:"final,omitempty"`           // 权威快照或收口残尾
}

// NewEventID 事件唯一标识（P1-05）：uuid 失败退化为时间戳纳秒（唯一性弱化但
// 不阻塞事件面）。UUIDv4 走 oekit/pkg/stringx 单源（v0.55.0 UUIDv4Err 错误
// 返回变体：本契约是「失败退化」而非 panic，与 webhook.NewEventID 同形态）。
func NewEventID() string {
	if id, err := stringx.UUIDv4Err(); err == nil {
		return id
	}
	return fmt.Sprintf("ev-%d", time.Now().UnixNano())
}
