// Package effect C1 副作用至多一次的宿主包装：kernel sideeffect.Runner 的
// fail-soft 外壳 + 环境开关 + 领域无感的类型别名。
//
// # 为什么还需要这一层
//
// kernel 的 Runner 在存储故障时是**变红**的（ErrNoStore / 原始 DB 错误），
// 这对「副作用闸」本身是对的；但评论/PR 创建是审查主链路的一步，存储面
// 短暂不可用时整条回帖失败会把一次本来能完成的审查变成 failed 任务。故本包
// 把**存储不可用**这一种失败降级为「直接施加、不承诺 at-most-once」，其余
// 判据（结果未知 / 栅栏失效 / 动作冲突）仍然变红——见 Do 的分类。
//
// # 开关
//
// ARGUS_SIDEEFFECT_MODE=off（大小写不敏感；0/false/disabled 同义）绕过闸，
// 缺省开启。绕过只用于排障，生产缺省必须开启。
package effect

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/oekit/logx"

	"github.com/yi-nology/goagent-kernel/sideeffect"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/storage"
)

// EnvMode 副作用闸开关（off 绕过；缺省开启）。
const EnvMode = "ARGUS_SIDEEFFECT_MODE"

// EventStoreUnavailable fail-soft 观测事件（存储不可用 / 开关关闭）。
const EventStoreUnavailable = "argus.sideeffect.store_unavailable"

// 建议 TTL：评论/PR 创建是秒级平台调用；分支推送含沙箱 commit/push，给足余量。
const (
	DefaultTTL = 2 * time.Minute
	PushTTL    = 5 * time.Minute
)

// 领域无感的类型别名：poster/runner 只 import 本包，不直接依赖 kernel。
type (
	// Action 一条待施加的动作（sideeffect.Action 的同义类型）。
	Action = sideeffect.Action
	// Key 幂等键。
	Key = sideeffect.Key
	// Lease 一次独占持有（含栅栏令牌，传给下游/日志用）。
	Lease = sideeffect.Lease
	// Result 动作结果（Payload 由宿主定义，本仓用 JSON）。
	Result = sideeffect.Result
	// Outcome Acquire 的判定结论。
	Outcome = sideeffect.Outcome
	// RetrySafety 施加失败后的重试安全性声明。
	RetrySafety = sideeffect.RetrySafety
)

const (
	// OutcomeApplied 本次获得执行权。
	OutcomeApplied = sideeffect.OutcomeApplied
	// OutcomeReplayed 动作已完成，取回既有结果，不执行。
	OutcomeReplayed = sideeffect.OutcomeReplayed
	// OutcomeContended 资源/Key 正被他人持有（可重试的拒绝）。
	OutcomeContended = sideeffect.OutcomeContended
	// OutcomeUncertain 上一次尝试结果未知（禁止重试，先对账）。
	OutcomeUncertain = sideeffect.OutcomeUncertain

	// RetrySafetyUnknown 未声明（按最弱处理）。
	RetrySafetyUnknown = sideeffect.RetrySafetyUnknown
	// RetrySafetySafe 下游按幂等键去重，可安全重试。
	RetrySafetySafe = sideeffect.RetrySafetySafe
	// RetrySafetyUnsafe 不可重试（重试会重复施加）。
	RetrySafetyUnsafe = sideeffect.RetrySafetyUnsafe
)

var (
	// ErrContended 本次未执行：动作/资源正被他人持有。可退避后重试（同一 Key
	// 会先取回对方的结果，不会重复施加）。
	ErrContended = errors.New("effect: 副作用正被他人持有，本次未执行")
	// ErrOutcomeUnknown 上一次尝试可能已生效：禁止重试，先对账。
	ErrOutcomeUnknown = sideeffect.ErrOutcomeUnknown
	// ErrLeaseLost 本次持有资格已失效（闸已易主）。
	ErrLeaseLost = sideeffect.ErrLeaseLost
)

// Runner 副作用闸的 fail-soft 外壳。零值不可用，请用 New/NewFromStore。
type Runner struct {
	inner   *sideeffect.Runner
	log     logx.Logger
	enabled bool
}

// New 组装包装器。st 为 nil 或 enabled=false 时 Do 走直通（fail-soft）。
func New(st sideeffect.Store, ttl time.Duration, log logx.Logger, enabled bool) *Runner {
	if log == nil {
		log = logx.Nop()
	}
	return &Runner{inner: sideeffect.NewRunner(st, ttl), log: log, enabled: enabled}
}

// NewFromStore 从 argus Store 组装（nil 安全）。poster/runner 的接线入口。
func NewFromStore(st *storage.Store, ttl time.Duration, log logx.Logger, enabled bool) *Runner {
	if st == nil {
		return New(nil, ttl, log, enabled)
	}
	return New(st.SideEffects(), ttl, log, enabled)
}

// Enabled 读取 ARGUS_SIDEEFFECT_MODE（缺省 on；off/0/false/disabled 绕过）。
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))) {
	case "off", "0", "false", "disabled":
		return false
	default:
		return true
	}
}

// Do 执行一次副作用。
//
// 失败分类（与 kernel Runner 的出口一一对应）：
//   - 存储不可用 / 开关关闭 → fail-soft：记 EventStoreUnavailable，用零 Lease
//     直接 apply，返回 Applied（**不承诺 at-most-once**）；
//   - OutcomeReplayed → 返回既有 Result，不 apply；
//   - OutcomeContended → 返回可重试的 ErrContended（调用方记日志/跳过重复）；
//   - OutcomeUncertain → 返回 ErrOutcomeUnknown（**不得重试**，先对账）；
//   - 其余错误（栅栏失效 / 动作冲突 / 施加失败）原样上抛。
func (r *Runner) Do(ctx context.Context, act Action, apply func(ctx context.Context, lease Lease) (Result, error)) (Outcome, Result, error) {
	if apply == nil {
		return OutcomeContended, Result{}, errors.New("effect: 副作用动作缺施加阶段")
	}
	if r == nil || !r.enabled {
		return r.direct(ctx, act, apply, "mode_off", nil)
	}
	if r.inner == nil {
		return r.direct(ctx, act, apply, "no_runner", nil)
	}
	// applyErr 记录执行体自己的失败：kernel 对 Safe 动作会把原始错误**不加**
	// ErrOutcomeUnknown 地返回，单看错误类型会把它误判成存储故障并再次施加。
	// 「执行体确实失败过」是比错误类型更可靠的判据。
	var applyErr error
	outcome, res, err := r.inner.Execute(ctx, act,
		func(ctx context.Context, lease Lease) (Result, error) {
			out, aerr := apply(ctx, lease)
			applyErr = aerr
			return out, aerr
		})
	if err != nil {
		if applyErr != nil && errors.Is(err, applyErr) {
			return outcome, res, err
		}
		if storageFailure(err) {
			return r.direct(ctx, act, apply, "store_unavailable", err)
		}
		if errors.Is(err, sideeffect.ErrOutcomeUnknown) {
			return OutcomeUncertain, res, err
		}
		return outcome, res, err
	}
	switch outcome {
	case OutcomeReplayed:
		return outcome, res, nil
	case OutcomeContended:
		return OutcomeContended, Result{}, ErrContended
	case OutcomeUncertain:
		return OutcomeUncertain, Result{}, sideeffect.ErrOutcomeUnknown
	default:
		return outcome, res, nil
	}
}

// direct fail-soft 直通：零 Lease 施加，结果/错误原样返回。
func (r *Runner) direct(ctx context.Context, act Action, apply func(context.Context, Lease) (Result, error), reason string, cause error) (Outcome, Result, error) {
	// fail-soft 直通意味着 at-most-once 不再成立：计数供运维观测（mode_off/
	// no_runner/store_unavailable）。
	obs.IncrSideEffectUnavailable(reason)
	if r != nil {
		args := []any{"key", string(act.Key), "resource", act.Resource, "reason", reason}
		if cause != nil {
			args = append(args, "error", cause.Error())
		}
		if r.log != nil {
			if reason == "mode_off" {
				r.log.Info(EventStoreUnavailable, args...)
			} else {
				r.log.Warn(EventStoreUnavailable, args...)
			}
		}
	}
	res, err := apply(ctx, Lease{})
	return OutcomeApplied, res, err
}

// storageFailure 是否属于「存储不可用」一档（可降级直通）。
//
// 语义哨兵（结果未知/栅栏失效/持有者不匹配/资源不一致/动作冲突/非法动作/未知
// 判定）**不**降级：它们不是故障，是判据本身；降级直通会绕过 at-most-once。
// 默认返回 true 只兜「未被识别的原始存储/DB 错误」。
func storageFailure(err error) bool {
	switch {
	case errors.Is(err, sideeffect.ErrNoStore):
		return true
	case errors.Is(err, sideeffect.ErrOutcomeUnknown),
		errors.Is(err, sideeffect.ErrLeaseLost),
		errors.Is(err, sideeffect.ErrFenceStale),
		errors.Is(err, sideeffect.ErrNotHolder),
		errors.Is(err, sideeffect.ErrResourceMismatch),
		errors.Is(err, sideeffect.ErrActionConflict),
		errors.Is(err, sideeffect.ErrInvalidAction),
		errors.Is(err, sideeffect.ErrUnknownOutcome),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return false
	default:
		return true
	}
}

// RetryBlocked 该错误是否属于「禁止盲目重试」一族（结果未知/栅栏已失效/
// 持有者或资源凭据不符/动作身份冲突/资源被占）。poster/runner 用它决定
// 「降级原地更新」是否安全：被闸挡住时任何降级重试都可能变成重复施加。
func RetryBlocked(err error) bool {
	return errors.Is(err, ErrContended) ||
		errors.Is(err, sideeffect.ErrOutcomeUnknown) ||
		// ErrUnknownOutcome 是存储侧返回了未定义结论（与 ErrOutcomeUnknown 的
		// 「本次尝试结果未知」不同）：同样是判据而非故障，重试前必须先对账。
		errors.Is(err, sideeffect.ErrUnknownOutcome) ||
		errors.Is(err, sideeffect.ErrLeaseLost) ||
		errors.Is(err, sideeffect.ErrFenceStale) ||
		errors.Is(err, sideeffect.ErrNotHolder) ||
		errors.Is(err, sideeffect.ErrResourceMismatch) ||
		errors.Is(err, sideeffect.ErrActionConflict)
}
