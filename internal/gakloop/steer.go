// steer.go —— 运行中接管（steer）吸收：协作中止与用户取消/停机可区分。
//
// 场景：审查任务执行中，控制面 cancel 或用户插入新指令时，不要把
// 「被接管」记成失败。steer.Signal 给 ctx 挂可区分的协作中止信号；
// 落终态前用 Classify 判三分（steered / cancelled / ok）。
package gakloop

import (
	"context"
	"errors"

	"github.com/oagentkit/goagent-kernel/steer"
)

// Cause 执行中断的三分判据。
type Cause int

const (
	// CauseOK 正常结束。
	CauseOK Cause = iota
	// CauseSteered 运行中被接管（协作中止；应回可认领态开新一轮）。
	CauseSteered
	// CauseCancelled 用户/停机取消（终态 failed 或取消语义）。
	CauseCancelled
)

func (c Cause) String() string {
	switch c {
	case CauseSteered:
		return "steered"
	case CauseCancelled:
		return "cancelled"
	default:
		return "ok"
	}
}

// Signal 包裹执行 ctx：返回可触发协作中止的 ctx 与触发函数。
// 与 context.WithCancel 兼容——调用方仍可用 cancel 收尾。
func Signal(parent context.Context) (context.Context, context.CancelFunc) {
	return steer.Signal(parent)
}

// IsSteered 是否被协作中止（与用户取消区分）。
func IsSteered(ctx context.Context) bool { return steer.IsSteered(ctx) }

// Classify 按 ctx 与 handler 错误判定中断原因：
//   - steer.ErrSteered / IsSteered → CauseSteered（保产续跑，不落 failed）
//   - ctx 取消且非 steer → CauseCancelled
//   - 其它 err → 原样（调用方按失败处理）
//
// 返回 (cause, err)：err 为需上抛的原始错误（steered 时为 nil——中止不是失败）。
func Classify(ctx context.Context, err error) (Cause, error) {
	if err == nil {
		return CauseOK, nil
	}
	if errors.Is(err, steer.ErrSteered) || IsSteered(ctx) {
		return CauseSteered, nil
	}
	if ctx.Err() != nil {
		return CauseCancelled, err
	}
	return CauseOK, err
}

// BuildSummary 保产摘要（已完成环节机械拼接，零 LLM）。
func BuildSummary(steps []steer.DoneStep, caps steer.Caps) string {
	return steer.BuildSummary(steps, caps)
}

// Decide 接管后续跑决策（有排队条目 → created 重认领；否则收敛）。
func Decide(hasQueue bool) steer.Decision { return steer.Decide(hasQueue) }
