// service_taskapi_events.go —— 任务/评论事件广播（自 service_taskapi.go 拆出）。

package service

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/storage"
)

// publishCreated 广播 task.created（nil Bus 安全）+ 落 outbox（P1-05，#10 跨实例
// 分发：他实例 fanout 环回灌本地 Bus）。
func (s *Service) publishCreated(t *storage.Task) {
	if s.Bus == nil || t == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: progress.EventTaskCreated, TaskID: t.ID, Platform: t.Platform,
		Owner: t.Owner, Repo: t.Repo, Number: t.Number,
	})
}

// publishProgress 进程内快路径广播 + outbox 兜跨实例（P1-05，#10）：本实例
// SSE 客户端走 Bus（实时），他实例的 fanout 环轮询 outbox 回灌其本地 Bus——
// 双源经 Event.ID 去重（v6.12 P1：本地发布侧 Bus.Publish 前登记实例级
// EventSeen 环，fanout 回灌命中环即跳过——同实例 at-most-once，修复评估实锤
// 的「同实例收到两次」竞窗）。outbox 追加失败只计数+告警（观测面数据，
// at-least-once 尽力而为，不阻断任务主流程）。
func (s *Service) publishProgress(ev progress.Event) {
	ev.ID = progress.NewEventID()
	if s.EventSeen != nil {
		s.EventSeen.Add(ev.ID) // 本地发布登记：fanout 回灌同 ID 跳过
	}
	s.Bus.Publish(ev)
	if s.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.AppendOutboxEvent(ctx, ev); err != nil {
		obs.IncrEventDropped("outbox_failed")
		s.log().Warn("argus.events.outbox_append_failed", "event", ev.ID, "type", ev.Type, "error", err.Error())
	}
}

// publishCommentReplied 广播 comment.replied（v3.50.0）：Q&A/意图路由回帖的终态
// 事件——此前评论链路无 Bus 事件，SSE 侧只见任务不见问答，观测口径分裂。
// status: ok=回帖成功 / error=回帖失败或 LLM 失败（nil Bus 安全）。
func (s *Service) publishCommentReplied(status, platform, owner, repo, number string) {
	if s.Bus == nil {
		return
	}
	s.publishProgress(progress.Event{
		Type: "comment.replied", Status: status, Platform: platform,
		Owner: owner, Repo: repo, Number: number,
	})
}
