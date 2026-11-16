// cancel.go —— 操作员终止的 runner 侧协作停机（#48）。
//
// 诚实边界：在途 LLM/工具调用无法强杀。取消只在下一个检查点生效；检查点之间
// 已经发生的平台写操作由既有 sideeffect 闸/幂等键兜底（重复执行会被 Key 级
// 重放/未知态挡住），不承诺「终止即零副作用」。
package runner

import (
	"context"
	"errors"

	"github.com/yi-nology/argus/internal/storage"
)

// ErrTaskCancelled 控制面已受理终止。handleRunFailure 对它的唯一动作是记
// info 日志——终态已由控制面写入，rerun 侧不得再写终态/公告/安排重试。
var ErrTaskCancelled = errors.New("runner: 任务已被操作员终止")

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
		return ErrTaskCancelled
	}
	return nil
}
