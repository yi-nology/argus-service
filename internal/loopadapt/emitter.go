package loopadapt

import (
	"context"

	"github.com/yi-nology/goagent-runtime/agentloop"

	"github.com/yi-nology/argus/internal/agentstream"
)

// EmitterFunc 把函数包成 agentloop.Emitter（事件分发的最小接缝：宿主按
// typ/payload 自行路由，Publish 恒成功——收敛器与轨迹日志不产错误）。
type EmitterFunc func(typ string, payload map[string]any)

// Publish 实现 agentloop.Emitter。
func (f EmitterFunc) Publish(_ context.Context, _, _ string, _ *uint64, typ, _ string, payload map[string]any) error {
	if f != nil {
		f(typ, payload)
	}
	return nil
}

// NewStreamEmitter agentloop 事件 → agentstream 收敛器（+ 可选工具轨迹
// 钩子）。streamer 可 nil（不要流式、只要轨迹的宿主，如 R3）；onToolCall
// 可 nil。payload 键 = agentloop 事件词表（kind/text/tool/outcome/bytes——
// bytes 为结果字节数，审批拒绝等无结果轮缺省 0）。
func NewStreamEmitter(s *agentstream.Streamer, onToolCall func(tool, outcome string, bytes int)) agentloop.Emitter {
	return EmitterFunc(func(typ string, payload map[string]any) {
		switch typ {
		case agentloop.EventContentDelta:
			if s == nil {
				return
			}
			kind, _ := payload["kind"].(string)
			text, _ := payload["text"].(string)
			s.OnDelta(kind, text)
		case agentloop.EventToolCall:
			if s != nil {
				s.OnToolCall()
			}
			if onToolCall != nil {
				tool, _ := payload["tool"].(string)
				outcome, _ := payload["outcome"].(string)
				bytes, _ := payload["bytes"].(int)
				onToolCall(tool, outcome, bytes)
			}
		}
	})
}
