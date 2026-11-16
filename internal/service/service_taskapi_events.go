// service_taskapi_events.go —— 任务/评论事件广播（自 service_taskapi.go 拆出）。

package service

import (
	"github.com/yi-nology/argus/internal/plugin"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// publishCreated 广播 task.created（nil Bus 安全）+ 落 outbox（P1-05，#10 跨实例
// 分发：他实例 fanout 环回灌本地 Bus）。
func (s *Service) publishCreated(t *storage.Task) { publishCreated(s, t) }

// publishProgress 进程内快路径广播 + outbox 兜跨实例（P1-05，#10）：本实例
// SSE 客户端走 Bus（实时），他实例的 fanout 环轮询 outbox 回灌其本地 Bus——
// 双源经 Event.ID 去重（v6.12 P1：本地发布侧 Bus.Publish 前登记实例级
// EventSeen 环，fanout 回灌命中环即跳过——同实例 at-most-once，修复评估实锤
// 的「同实例收到两次」竞窗）。outbox 追加失败只计数+告警（观测面数据，
// at-least-once 尽力而为，不阻断任务主流程）。
func (s *Service) publishProgress(ev progress.Event) { publishProgress(s, ev) }

// publishCommentReplied 广播 comment.replied（v3.50.0）：Q&A/意图路由回帖的终态
// 事件——此前评论链路无 Bus 事件，SSE 侧只见任务不见问答，观测口径分裂。
// status: ok=回帖成功 / error=回帖失败或 LLM 失败（nil Bus 安全）。
func (s *Service) publishCommentReplied(status, platform, owner, repo, number string) {
	publishCommentReplied(s, status, platform, owner, repo, number)
}

// publishQADelta 广播 QA ReAct 流式增量（P3 opt-in，ARGUS_REACT_STREAM）。
// **只走本地 Bus，不落 outbox**：delta 是 live 观测面，跨实例 SSE 不保证；
// TaskID 为空（问答不是任务），Platform/Owner/Repo/Number 填齐供 PR 过滤。
// nil Bus 安全。
func (s *Service) publishQADelta(platform, owner, repo, number string, d plugin.AgentDelta) {
	publishQADelta(s, platform, owner, repo, number, d)
}
