// service_impl_eventfanout.go —— Service 方法体的包级实现（转发壳见原文件）。

package service

import (
	"context"
	"time"

	"github.com/oekit/concurrency/async"

	"github.com/yi-nology/argus/internal/progress"
	"github.com/yi-nology/argus/internal/tenant"
)

// startOutboxFanout 的包级实现（转发壳见原文件）。
func startOutboxFanout(s *Service, ctx context.Context) {
	if s.Bus == nil || s.Store == nil {
		return
	}
	// fanout 是跨租户分发机制：本实例的 SSE 客户端可能属于任何租户，事件谓词
	// 必须显式 WithoutScope（#109c——否则租户回调注入 default 谓词，非 default
	// 租户事件永不 fanout、其 outbox 行也永不清理：两个潜伏的多租户正确性
	// 缺陷）。按消费者过滤在 SSE authz 层，不在分发层。
	fanoutCtx := tenant.WithoutScope(ctx)
	// 基线游标：从"现在"开始，不重播历史（重启不回放旧事件——SSE 是实时面）。
	lastID, err := s.Store.MaxOutboxID(fanoutCtx)
	if err != nil {
		s.log().Warn("argus.events.fanout_baseline_failed", "error", err.Error())
		lastID = 0
	}
	// v6.12 P1：去重环从局部提升为实例级共享组件（Service.EventSeen，bootstrap
	// 装配与 Runner 共享同一实例）——本地发布侧 publishProgress/publishOutbox
	// 登记 ID，回灌命中环即跳过，同实例 at-most-once。nil 时退回局部环（测试栈
	// 独立构造 Service 的场景，语义不变：fanout 单协程独占仍去重，只是本地发布
	// 侧不登记）。
	seen := s.EventSeen
	if seen == nil {
		seen = progress.NewDedup(outboxSeenCap)
	}
	// async.GoSafe（B3）：panic 转留痕不进程死；闭包捕获 ctx/seen，取消语义不变。
	async.GoSafe(func() {
		prune := time.NewTicker(time.Hour)
		defer prune.Stop()
		t := time.NewTicker(outboxFanoutInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-prune.C:
				if n, err := s.Store.PruneOutboxEvents(fanoutCtx, time.Now().Add(-outboxRetention)); err != nil {
					s.log().Warn("argus.events.outbox_prune_failed", "error", err.Error())
				} else if n > 0 {
					s.log().Info("argus.events.outbox_pruned", "removed", n)
				}
				// #67：agent.delta 行独立短保留期（体量远大于状态事件，价值只在
				// 流式窗口内——1h 足够跨实例 fanout 消化），防 outbox 膨胀。
				if n, err := s.Store.PruneDeltaOutboxEvents(fanoutCtx, time.Now().Add(-outboxDeltaRetention)); err != nil {
					s.log().Warn("argus.events.outbox_delta_prune_failed", "error", err.Error())
				} else if n > 0 {
					s.log().Info("argus.events.outbox_delta_pruned", "removed", n)
				}
			case <-t.C:
				rows, err := s.Store.ListOutboxEvents(fanoutCtx, lastID, 200)
				if err != nil {
					s.log().Warn("argus.events.fanout_poll_failed", "error", err.Error())
					continue
				}
				for _, r := range rows {
					if r.ID > lastID {
						lastID = r.ID
					}
					if r.Event.ID == "" || !seen.Add(r.Event.ID) {
						continue // 本实例已广播过（v6.12：本地发布侧登记）或行缺 ID（坏行）
					}
					s.Bus.Publish(r.Event)
				}
			}
		}
	})
	s.log().Info("argus.events.fanout_started", "baseline_id", lastID, "interval", outboxFanoutInterval.String())
}
