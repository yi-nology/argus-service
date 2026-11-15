package storage

import (
	"context"
)

// LatestDoneTask 某 PR 最近一次成功审查的任务（ask_argus 数据源）。
// 不存在返回 nil, nil。
func (s *Store) LatestDoneTask(ctx context.Context, platform, owner, repo, number string) (*Task, error) {
	return latestDoneTask(s, ctx, platform, owner, repo, number)
}
