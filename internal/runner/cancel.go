// cancel.go —— 操作员终止的 runner 侧协作停机（#48）+ steer 吸收（T12）。
//
// 诚实边界：在途 LLM/工具调用无法强杀。取消只在下一个检查点生效；检查点之间
// 已经发生的平台写操作由既有 sideeffect 闸/幂等键兜底（重复执行会被 Key 级
// 重放/未知态挡住），不承诺「终止即零副作用」。
//
// steer（gakloop）：控制面终止同时触发**协作中止信号**——下游 ReAct/子代理
// 可用 gakloop.IsSteered 与「用户取消/进程停机」区分：被接管/被终止时应
// 保产续跑或干净收尾，而不是把摘要丢进 failed。
package runner

import (
	"context"
	"errors"

	"github.com/yi-nology/argus/internal/gakloop"
	"github.com/yi-nology/argus/internal/storage"
)

// ErrTaskCancelled 控制面已受理终止。handleRunFailure 对它的唯一动作是记
// info 日志——终态已由控制面写入，rerun 侧不得再写终态/公告/安排重试。
var ErrTaskCancelled = errors.New("runner: 任务已被操作员终止")

// steerFireKey 协作中止触发器的 ctx 键（execute 注入）。
type steerFireKey struct{}

// withSteerFire 注入协作中止触发器。
func withSteerFire(ctx context.Context, fire context.CancelFunc) context.Context {
	return context.WithValue(ctx, steerFireKey{}, fire)
}

// fireSteer 触发协作中止信号（未注入时 no-op）。
func fireSteer(ctx context.Context) {
	if f, ok := ctx.Value(steerFireKey{}).(context.CancelFunc); ok && f != nil {
		f()
	}
}

// checkCancelled 只读任务终止标记：命中返回 ErrTaskCancelled。
//
// DB 错误 fail-open（记录 warn 后继续）：检查点是「尽快停」的协作优化，不是
// 正确性防线；把一次临时查询失败升级成任务失败会误伤正常执行。真正的正确性
// 由控制面的条件更新（status 已是 failed → runner 的 FinishTaskFenced 被围栏）
// 与 sideeffect 闸承担。
func (r *Runner) checkCancelled(ctx context.Context, task *storage.Task) error {
	requested, err := r.Store.TaskCancelRequested(ctx, task.ID)
	if err != nil {
		r.Log.Warn("argus.runner.cancel_check_failed", "task", task.ID, "error", err.Error())
		return nil
	}
	if requested {
		// steer 吸收：先发协作中止，再报终止——下游能区分「被终止」与「进程挂了」。
		fireSteer(ctx)
		return ErrTaskCancelled
	}
	return nil
}

// classifyRunEnd 运行收尾的三分判据（gakloop.Classify 门面）：
// steered ≠ cancelled ≠ 失败。返回 log 用的 cause 字符串。
func classifyRunEnd(ctx context.Context, err error) string {
	cause, _ := gakloop.Classify(ctx, err)
	return cause.String()
}
