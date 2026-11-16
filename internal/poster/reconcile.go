// reconcile.go —— 对账用评论查询：按任务 ID 在 PR 评论里找报告头标记。
//
// 只认报告头 `argus:report task_id=<id> `（带尾随空格，避免 task_id=t-1 命中
// task_id=t-10）。`argus:start` 是“正在审查”公告 marker，与总结评论是否落库
// 无关，不能拿来做生效判定。
package poster

import (
	"context"
	"fmt"
	"strings"
)

// FindTaskComment 返回携带指定任务报告头的首条评论 ID。found=false 且
// err=nil 表示列表查询成功但没有该标记（显式「未生效」证据）。
func (p *Poster) FindTaskComment(ctx context.Context, platform, owner, repo, number, taskID string) (int64, bool, error) {
	if p == nil || p.SDK == nil {
		return 0, false, fmt.Errorf("poster: SDK 未装配，无法查询任务评论")
	}
	if taskID == "" {
		return 0, false, fmt.Errorf("poster: task_id 为空，无法匹配报告评论")
	}
	comments, err := p.SDK.ListIssueComments(ctx, platform, owner, repo, number)
	if err != nil {
		return 0, false, fmt.Errorf("poster: 查询评论列表失败: %w", err)
	}
	marker := "argus:report task_id=" + taskID + " "
	for _, c := range comments {
		if c == nil {
			continue
		}
		if strings.Contains(c.Body, marker) {
			return c.ID, true, nil
		}
	}
	return 0, false, nil
}
