// writing.go 写作面任务执行（v3.71.0）：describe 生成并写回 PR 描述、
// changelog 生成变更条目并发评论。不走 R 系列审查管线与插件派发——
// 单步 LLM 写作后直接平台写回，复用 runner 的 marker/落库/播报原语收口。

package runner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yi-nology/go-git-platform/provider"

	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/storage"
	"github.com/yi-nology/argus/internal/writing"
)

// runWriting 写作任务主链：prepareContext 之后由 run 分派进入。
// 产物不进 reportview 审查模板——report 正文即写作产物，经 FinishTaskFenced
// 落库（GET /tasks/:id 可查）。
func (r *Runner) runWriting(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	diff *plugin.DiffContext, start time.Time) error {
	return runWriting(r, ctx, task, pr, diff, start)
}

// writeDescribe describe 写作 + 描述写回（管理段替换/追加，不覆盖作者内容）。
func (r *Runner) writeDescribe(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*plugin.WorkflowOutput, error) {
	return writeDescribe(r, ctx, task, pr, svc, in, prov)
}

// writeChangelog changelog 条目写作 + 评论发布。
func (r *Runner) writeChangelog(ctx context.Context, task *storage.Task, pr *plugin.PRContext,
	svc *writing.Service, in writing.Input, prov provider.Provider) (*plugin.WorkflowOutput, int64, error) {
	return writeChangelog(r, ctx, task, pr, svc, in, prov)
}

// writingTargetLabel 收口播报里产物去向的措辞。
func writingTargetLabel(kind string, commentID int64) string {
	if commentID > 0 {
		return "评论 #" + fmt.Sprintf("%d", commentID)
	}
	if kind == domain.TaskKindDescribe {
		return "PR 描述"
	}
	return "任务报告"
}

// parseCommentID CreateDiscussion 返回的平台评论 ID（非数字=0，容忍降级）。
func parseCommentID(idStr string) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
