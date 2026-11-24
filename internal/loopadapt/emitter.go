package loopadapt

import (
	"context"

	"github.com/yi-nology/goagent-runtime/agentloop"
	"github.com/yi-nology/goagent-runtime/pipeline"

	"github.com/yi-nology/argus/internal/agentstream"
)

// EmitterFunc 把函数包成 agentloop.Emitter（事件分发的最小接缝：宿主按
// typ/payload 自行路由，Publish 恒成功——收敛器不产错误）。
type EmitterFunc func(typ string, payload map[string]any)

// Publish 实现 agentloop.Emitter。
func (f EmitterFunc) Publish(_ context.Context, _, _ string, _ *uint64, typ, _ string, payload map[string]any) error {
	if f != nil {
		f(typ, payload)
	}
	return nil
}

// NewStreamEmitter agentloop 事件 → agentstream 收敛器。只认
// content_delta（kind/text）与 tool_call（轮次推进）；round/session 事件
// 与 SSE 流无关，忽略。DeltaSink nil 的宿主不挂 Emitter（零开销、模型走
// Generate）——工具轨迹不走事件面，见 TraceTool。
func NewStreamEmitter(s *agentstream.Streamer) agentloop.Emitter {
	return EmitterFunc(func(typ string, payload map[string]any) {
		switch typ {
		case agentloop.EventContentDelta:
			kind, _ := payload["kind"].(string)
			text, _ := payload["text"].(string)
			s.OnDelta(kind, text)
		case agentloop.EventToolCall:
			s.OnToolCall()
		}
	})
}

// TraceTool 工具轨迹审计装饰（单源纪律 = plugin.ToolTraceLogger 的
// 「只记长度不记内容」）：包一层 Handler，执行完落 tool/bytes。挂在
// pipeline 而非事件面——与流式开关解耦（DeltaSink nil 时 Emitter 不挂，
// 轨迹照走；旧 agentrun 的 tool_call/tool_result 两行合一，outcome 维度
// 随事件面退役、bytes 由 Handler 结果直取）。
func TraceTool(t pipeline.Tool, trace func(tool string, bytes int)) pipeline.Tool {
	if trace == nil {
		return t
	}
	inner := t.Handler
	t.Handler = func(ctx context.Context, args []byte) (string, error) {
		out, err := inner(ctx, args)
		trace(t.Name, len(out))
		return out, err
	}
	return t
}
