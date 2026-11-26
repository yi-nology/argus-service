// Package loopadapt —— 宿主迁 goagent-runtime agentloop 的共用接缝。
//
// QA ReAct、builtin 审查、requirement R3 三条 agentrun→agentloop 迁移线
// 共用的适配件单源于此，防三处各写一份漂移：
//
//   - einoChat：eino BaseChatModel → chat.Model（OnDelta nil 走 Generate /
//     非 nil 走 Stream 逐片，增量契约信任面与 agentrun streamDeltas 对齐）；
//   - ToPipelineTools：eino 工具表 → pipeline（只读表 Safe=true 的断言在
//     调用方，本函数只做形态转换与可执行体校验）；
//   - EmitterFunc / NewStreamEmitter：agentloop 事件 → agentstream 收敛器
//     （+ 可选工具轨迹钩子），宿主按需组合。
//
// 信任面与 agentrun 对齐：流式遵循 eino schema/stream 增量契约（分片即
// 增量、ConcatMessages 拼接）；关流 defer+once 恰好一次（agentrun 同款
// 教训 oagentkit/agent#77）；调用轮带 tool_calls 的正文分片不外发。
package loopadapt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"

	"github.com/yi-nology/goagent-runtime/chat"
	"github.com/yi-nology/goagent-runtime/pipeline"
)

// einoChat eino BaseChatModel → chat.Model 模型接缝（构造见 NewEinoChat）。
type einoChat struct {
	m model.BaseChatModel
}

// NewEinoChat 构造 eino 模型适配。
//
// OnDelta nil = 非流式（Generate 整段——DeltaSink 关闭的回滚开关语义，
// 与旧 DisableStreaming 等价）；非 nil = Stream 逐片回调，终答仍经
// ChatResponse 返回（回调不改变响应语义）。
func NewEinoChat(m model.BaseChatModel) chat.Model {
	return einoChat{m: m}
}

// Chat 实现 chat.Model（见 NewEinoChat 注释）。
func (c einoChat) Chat(ctx context.Context, req chat.ChatRequest) (chat.ChatResponse, error) {
	msgs := make([]*schema.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, toSchemaMessage(m))
	}
	opts, err := modelToolOpts(req.Tools)
	if err != nil {
		return chat.ChatResponse{}, err
	}
	if req.OnDelta == nil {
		out, gerr := c.m.Generate(ctx, msgs, opts...)
		if gerr != nil {
			return chat.ChatResponse{}, gerr
		}
		return responseFrom(out), nil
	}
	return c.stream(ctx, msgs, opts, req.OnDelta)
}

// stream 流式分支：逐分片回调 → 合并整消息回 ChatResponse。
func (c einoChat) stream(ctx context.Context, msgs []*schema.Message, opts []model.Option, onDelta func(chat.Delta)) (chat.ChatResponse, error) {
	sr, err := c.m.Stream(ctx, msgs, opts...)
	if err != nil {
		return chat.ChatResponse{}, err
	}
	// eino 契约（schema/stream.go）：Close 恰好一次——EOF/错误/取消/panic
	// 全退出路径 defer+once 兜住（once 兜 Close 不幂等）。
	var closeOnce sync.Once
	defer func() { closeOnce.Do(func() { sr.Close() }) }()
	var chunks []*schema.Message
	for {
		chunk, cerr := sr.Recv()
		if cerr == io.EOF {
			break
		}
		if cerr != nil {
			return chat.ChatResponse{}, fmt.Errorf("loopadapt: 流式消息接收失败: %w", cerr)
		}
		if chunk == nil {
			continue
		}
		chunks = append(chunks, chunk)
		if chunk.Role != schema.Assistant {
			continue
		}
		// 携带 tool_calls 的正文分片不外发（调用轮拼接物非可读答复；其前导
		// 正文分片不带 tool_calls，会先行外发）——agentrun streamDeltas 同款。
		if chunk.ReasoningContent != "" {
			onDelta(chat.Delta{Kind: chat.DeltaKindReasoning, Text: chunk.ReasoningContent})
		}
		if chunk.Content != "" && len(chunk.ToolCalls) == 0 {
			onDelta(chat.Delta{Kind: chat.DeltaKindText, Text: chunk.Content})
		}
	}
	if len(chunks) == 0 {
		// 零分片 = 传输/网关侧断流（连接开即断），与模型真空答复两类成因
		// 分开——agentrun 同款：显式上抛走回喂重试，不放大成空成功。
		return chat.ChatResponse{}, fmt.Errorf("loopadapt: 流式读取零分片（传输/网关侧断流，非模型空答复）")
	}
	full, err := schema.ConcatMessages(chunks)
	if err != nil {
		return chat.ChatResponse{}, fmt.Errorf("loopadapt: 流式消息合并失败: %w", err)
	}
	return responseFrom(full), nil
}

// modelToolOpts 工具面 → eino 调用选项（chat.ToolSpec.Parameters 为
// JSON Schema 文本，解析回 *jsonschema.Schema 走 NewParamsOneOfByJSONSchema）。
func modelToolOpts(specs []chat.ToolSpec) ([]model.Option, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	infos := make([]*schema.ToolInfo, 0, len(specs))
	for _, s := range specs {
		info := &schema.ToolInfo{Name: s.Name, Desc: s.Description}
		if s.Parameters != "" {
			var sc jsonschema.Schema
			if err := json.Unmarshal([]byte(s.Parameters), &sc); err != nil {
				return nil, fmt.Errorf("loopadapt: 工具 %s 参数模式解析失败: %w", s.Name, err)
			}
			info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(&sc)
		}
		infos = append(infos, info)
	}
	return []model.Option{model.WithTools(infos)}, nil
}

// toSchemaMessage chat.Message → eino schema.Message（角色词表同串直转；
// tool_call ID/名/参数原样——ID 是幂等键原材料，不得改写）。
func toSchemaMessage(m chat.Message) *schema.Message {
	out := &schema.Message{
		Role:       schema.RoleType(m.Role),
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
	}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, schema.ToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: schema.FunctionCall{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return out
}

// responseFrom eino 消息 → chat.ChatResponse（ToolCalls 非空 = 进工具回合）。
func responseFrom(m *schema.Message) chat.ChatResponse {
	if m == nil {
		return chat.ChatResponse{}
	}
	out := chat.ChatResponse{Content: m.Content}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, chat.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return out
}

// 重试安全断言（pipeline.Tool.Safe 的调用方口径）：
const (
	// SafeReadOnly 调用方断言表内工具全为只读读者——幂等由「只读」保证，
	// 失败是普通错误回注、不进未知态对账面。QA/审查/R3 三张 ReAct 表均属此类；
	// 若往表里加工具，先问它施加不施加副作用，施加的不得用本断言。
	SafeReadOnly = true
	// SafeMutating 未断言幂等——失败按最弱处理（结果未知须先对账）。
	SafeMutating = false
)

// ToPipelineTools eino 工具表 → runtime pipeline（safe 见 SafeReadOnly/
// SafeMutating）；非 Invokable（无可执行体）直接装配期报错——pipeline
// 没有第二执行形态。
func ToPipelineTools(ctx context.Context, tools []tool.BaseTool, safe bool) ([]pipeline.Tool, error) {
	out := make([]pipeline.Tool, 0, len(tools))
	for _, bt := range tools {
		info, err := bt.Info(ctx)
		if err != nil {
			return nil, fmt.Errorf("loopadapt: 工具元数据读取失败: %w", err)
		}
		inv, ok := bt.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("loopadapt: 工具 %s 非 Invokable（pipeline 只接可执行体）", info.Name)
		}
		var params json.RawMessage
		if info.ParamsOneOf != nil {
			sc, err := info.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("loopadapt: 工具 %s 参数模式导出失败: %w", info.Name, err)
			}
			if sc != nil {
				b, err := sc.MarshalJSON()
				if err != nil {
					return nil, fmt.Errorf("loopadapt: 工具 %s 参数模式序列化失败: %w", info.Name, err)
				}
				params = b
			}
		}
		out = append(out, pipeline.Tool{
			Name:        info.Name,
			Description: info.Desc,
			Parameters:  params,
			Safe:        safe,
			Handler: func(hctx context.Context, args []byte) (string, error) {
				return inv.InvokableRun(hctx, string(args))
			},
		})
	}
	return out, nil
}
