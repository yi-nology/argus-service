// Package effect 修复链执行结果分类哨兵（#51 防御性收口）：push/CR 路径对
// 「kernel/store 侧未定义 outcome」的反应必须显式——不盲重试、不静默降级、
// 不把 FixStatus 永远留在 pushing。
//
// 两个哨兵刻意区分（issue #51 验收）：
//   - ErrOutcomeUnknown（对齐 goagent-kernel sideeffect.ErrOutcomeUnknown 语义）：
//     **动作的结果未知**——上次尝试可能已生效也可能没生效，原样重试有重复
//     应用风险（fix.push 的 add/commit/push 即此面）；
//   - ErrUnknownOutcome：**副作用闸/存储侧返回了未定义结论**——不是动作本身
//     未知，而是裁决者没给出定义内判定（GetTask 失败、状态机返回越界值等），
//     连「上次是否生效」都无从判断。
//
// 两者同判 RetryBlocked=true（都不可盲重试，先对账/换路径），但哨兵不同——
// 排障面要求能 errors.Is 区分「哪一侧失明」。当前调用点（修复链归属检查）
// 不可达未知形态，属防御性收口：真发生时 push/CR 路径落显式终态而不是静默
// 返回（此前会继续推送或把 FixStatus 停在 pushing）。
package effect

import "errors"

// ErrOutcomeUnknown 动作的**结果未知**：上一次尝试可能已经生效，也可能没有。
// 原样重试有重复应用风险（重复 push / 重复 commit）。
var ErrOutcomeUnknown = errors.New("effect: 动作结果未知：上次尝试可能已生效，原样重试有重复应用风险")

// ErrUnknownOutcome 副作用闸/存储侧返回了**未定义的结论**：裁决者失明，
// 连上次尝试是否生效都无从判断（与 ErrOutcomeUnknown 区分，见包 doc）。
var ErrUnknownOutcome = errors.New("effect: 存储侧未定义结论：不可作为继续/重试依据")

// Class 一次存储/kernel 面失败的处置分类。
type Class struct {
	// Degrade 可降级：失败留痕后按既有 best-effort 面继续，不阻断主链路。
	Degrade bool
	// RetryBlocked 禁止盲重试/盲继续：先对账或换处置路径，重试可能放大副作用
	// （重复推送、越权僵尸写）。
	RetryBlocked bool
}

// StorageFailure 存储面失败分类（issue #51 的 storageFailure）：
//   - 未知结论（ErrUnknownOutcome）与未知结果（ErrOutcomeUnknown）一律
//     **non-degrade + RetryBlocked**——失明状态下继续/重试都是赌；
//   - 其余失败（普通存储抖动/连接失败）可降级、不禁重试：按既有重试面走。
//   - nil 归零值（无失败无处置）。
func StorageFailure(err error) Class {
	switch {
	case err == nil:
		return Class{}
	case errors.Is(err, ErrUnknownOutcome), errors.Is(err, ErrOutcomeUnknown):
		return Class{Degrade: false, RetryBlocked: true}
	default:
		return Class{Degrade: true, RetryBlocked: false}
	}
}

// RetryBlocked 报告该失败是否禁止盲重试/盲继续（push/CR 路径守卫，issue #51）。
// errors.Is 穿透包裹链：sentinel 被任意层 wrap 后判定不丢。
func RetryBlocked(err error) bool {
	return StorageFailure(err).RetryBlocked
}
