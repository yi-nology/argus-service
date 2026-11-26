// react.go —— ReAct 运行接线单源（goagent-runtime agentloop/pipeline）。
//
// QA / builtin 审查 / requirement R3 三条线此前各自手写同一段装配：
// 每 attempt 重建工具表 → ToPipelineTools → 内存 checkpoint/effects →
// pipeline.New → agentloop.New → Run；失败/空终答回喂重试一次；流式
// reset/终稿快照。三份重复会漂移（预算轮数、重试语义、未知态处置），本文件
// 收为宿主唯一接线点：判据仍全在 runtime/kernel，本层只装配。

package loopadapt

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/oagentkit/agent/roundguard"
	"github.com/yi-nology/goagent-kernel/contract"
	"github.com/yi-nology/goagent-runtime/agentloop"
	"github.com/yi-nology/goagent-runtime/chat"
	"github.com/yi-nology/goagent-runtime/pipeline"
	rtstore "github.com/yi-nology/goagent-runtime/storage"
)

// ChatModel runtime chat.Model 别名：调用方装配 ReActSpec 时无需直接 import
// runtime/chat（依赖收口在本包）。
type ChatModel = chat.Model

// ReActSpec 一次 ReAct 运行的宿主装配参数（判据在 runtime/kernel，本层只接线）。
type ReActSpec struct {
	// SessionPrefix 会话前缀（内部追加纳秒保证每次调用新会话；内存快照只保
	// 本次 Run 内自洽——跨重启状态归宿主存储）。
	SessionPrefix string
	// Model 每 attempt 重新取模型（装配层可能包 Failover/阶段覆盖/流式适配）。
	Model func() ChatModel
	// Tools 每 attempt 重建工具表（toolprior 限流计数按尝试重置的原语义）。
	Tools func(context.Context) ([]tool.BaseTool, error)
	// System 系统提示（新会话第一条消息）。
	System string
	// MaxRounds 回合预算（<=0 = runtime 不限额，调用方负责兜底）。
	MaxRounds int
	// Emitter 事件面；nil = 非流式（不产增量事件，模型走非流式分支）。
	Emitter agentloop.Emitter
	// Trace 工具轨迹装饰（nil = 不装饰）。
	Trace func(pipeline.Tool) pipeline.Tool
	// RetryHint 失败/非成功/空终答后整程重试追加的提示。
	RetryHint string
	// OnReset 重试前回调（流式收敛器清缓冲）。
	OnReset func()
	// OnFinal 成功终答回调（流式终稿快照；参数为未 trim 的 Final）。
	OnFinal func(string)
	// OnStats 运行遥测回调（v8.6）：每次 RunReAct 恰好一次，含回喂重试的
	// 累加轮数/工具调用；成功与失败路径都会上报。
	OnStats func(ReActStats)
	// Label 错误文案前缀（"ReAct"|"react"|"R3"）。
	Label string
}

// ReActStats 一次 RunReAct 的遥测快照（Judge 口径见 RunReAct 注释）：
//   - Attempts 实际 attempt 数（1=首跑成功；2=回喂重试过）；
//   - Rounds/ToolCalls 各 attempt 累加（重试成本可见）；
//   - Uncertain 未知态工具调用总数（>0 即 fail-closed 返回）；
//   - Status 最后一次 attempt 的终态；运行错误时 "error"。
type ReActStats struct {
	Attempts  int
	Rounds    int
	ToolCalls int
	Uncertain int
	Status    string
}

// DefaultToolTTL 工具副作用闸租约（只读工具秒级；与历史三处一致）。
const DefaultToolTTL = time.Minute

// RunReAct 用 runtime agentloop 执行一次可恢复回合循环：
//  1. 首跑；
//  2. 运行错误 / 非 succeeded / 空终答 → OnReset + 带 RetryHint 整程重跑一次
//     （新会话/新工具表/新快照；只读工具表重试安全）；
//  3. 仍败上抛。
//
// 为什么不用 runtime.NewEmbedded 模板：该模板的事件面经 eventbus + observe
// 异步转发（订阅 goroutine + 缓冲 channel），而流式增量与终稿快照必须与
// Run 同步有序（OnFinal/Close 前消费完，否则尾片丢失/乱序）。故事件面直接
// 接 agentloop.Emitter；存储与工具管线仍用 runtime 的 Mem/pipeline。
//
// 未知态工具调用（res.Uncertain）一律 fail-closed，**不盲目重试**（runtime
// 纪律：结果未知须先对账，重试可能二次施加）。当前三条线工具表均声明
// SafeReadOnly，内存闸下 uncertain 不应出现；守卫在此防未来接入副作用工具。
func RunReAct(ctx context.Context, spec ReActSpec, user string) (string, error) {
	label := spec.Label
	if label == "" {
		label = "ReAct"
	}
	if spec.Model == nil || spec.Tools == nil {
		return "", fmt.Errorf("%s: 模型或工具表未装配", label)
	}
	sessionID := fmt.Sprintf("%s-%d", spec.SessionPrefix, time.Now().UnixNano())

	// 遥测累加：每次 attempt 记录轮数/工具调用，重试成本可见；成功与失败
	// 路径均经 defer 上报一次。
	stats := ReActStats{}
	defer func() {
		if spec.OnStats != nil {
			spec.OnStats(stats)
		}
	}()
	record := func(res *agentloop.Result, err error) {
		stats.Attempts++
		if res != nil {
			stats.Rounds += res.Rounds
			stats.ToolCalls += res.ToolCalls
			stats.Uncertain += len(res.Uncertain)
			stats.Status = res.Status
		}
		if err != nil {
			stats.Status = "error"
		}
	}

	run := func(u string) (*agentloop.Result, error) {
		tools, err := spec.Tools(ctx)
		if err != nil {
			return nil, err
		}
		pt, err := ToPipelineTools(ctx, tools, SafeReadOnly)
		if err != nil {
			return nil, err
		}
		if spec.Trace != nil {
			for i := range pt {
				pt[i] = spec.Trace(pt[i])
			}
		}
		mem := rtstore.NewMem()
		pipe, err := pipeline.New(mem.Effects(), DefaultToolTTL, pt)
		if err != nil {
			return nil, err
		}
		loop, err := agentloop.New(agentloop.Config{
			Model:   spec.Model(),
			Tools:   pipe,
			Store:   mem.Checkpoints(),
			Emitter: spec.Emitter,
			Budget:  roundguard.Budget{MaxRounds: spec.MaxRounds},
			System:  spec.System,
		})
		if err != nil {
			return nil, err
		}
		return loop.Run(ctx, sessionID, u)
	}

	res, err := run(user)
	record(res, err)
	// 未知态先于重试判定：第一跑已出现 uncertain 时不得整程重跑。
	if res != nil && len(res.Uncertain) > 0 {
		return "", resultError(label, res)
	}
	if err != nil || resultError(label, res) != nil {
		if spec.OnReset != nil {
			spec.OnReset()
		}
		res, err = run(user + spec.RetryHint)
		record(res, err)
	}
	if err != nil {
		return "", fmt.Errorf("%s 运行失败: %w", label, err)
	}
	if rerr := resultError(label, res); rerr != nil {
		return "", rerr
	}
	final := strings.TrimSpace(res.Final)
	if spec.OnFinal != nil {
		spec.OnFinal(res.Final)
	}
	return final, nil
}

// resultError 一次 Run 结果是否可用（nil = 可用）。纯函数便于单测：
// uncertain > 非 succeeded > 空终答 > nil 结果。
func resultError(label string, res *agentloop.Result) error {
	if res == nil {
		return fmt.Errorf("%s 无结果", label)
	}
	if len(res.Uncertain) > 0 {
		return fmt.Errorf("%s 出现未知态工具调用（%s），禁止盲目重试",
			label, strings.Join(res.Uncertain, ","))
	}
	if res.Status != contract.StatusSucceeded {
		return fmt.Errorf("%s 未达终态: %s", label, res.Status)
	}
	if strings.TrimSpace(res.Final) == "" {
		return fmt.Errorf("%s 空回答", label)
	}
	return nil
}
