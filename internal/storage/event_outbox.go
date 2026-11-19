// event_outbox.go —— 事件 Outbox（v6.9.0 P1-05，#10）：进度事件的跨实例可靠
// 分发事实源。SSE 事件总线此前纯进程内（Bus 广播），多副本下连接 Pod A 的
// 客户端收不到 Pod B 上的任务事件。Outbox 模式：发布点在进程内广播的同时落
// outbox 表（best-effort 追加写，见下注），每实例一个 fanout 环轮询新行回灌
// 本地 Bus——SSE handler 不变（Bus 订阅 + AuthorizeEvent 按仓过滤），跨实例
// 事件经 outbox → fanout → 本地 Bus 送达。
//
// 语义边界：outbox 追加写是发布点事务外的独立小写（progress 事件是观测面
// 数据，at-least-once + 极小丢失窗可接受；任务一致性不依赖事件）。不引入
// 消息中间件（审计明确不建议）——PostgreSQL 轮询已满足跨副本送达；LISTEN/
// NOTIFY 是延迟优化预留（500ms 轮询已低于 SSE keepalive 粒度）。

package storage

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/progress"
)

// OutboxEvent 事件 outbox 行：单调 ID 分页游标 + 事件载荷。
type OutboxEvent struct {
	ID   int64  `gorm:"column:id;primaryKey;autoIncrement"`
	Name string `gorm:"column:event_id;size:64;index"` // progress.Event.ID（去重键）
	Body string `gorm:"column:body;type:text"`         // Event JSON
	// Platform/Owner/Repo 冗余列：按仓过滤的行级查询入口（审计对账/运维排查
	// 不需解 JSON）；Type 事件类型。TenantID 事件租户归属（fanout 谓词注入列）。
	//
	// #109c 显式约定：三列**刻意可空**——空坐标 = 本地/系统级事件（非任何
	// 仓库归属：全局运维事件、local 形态的进度事件）。强上 NOT NULL 会逼出
	// 假坐标（空串/- 占位串）破坏按仓过滤语义；「可空 + 本约定」即契约。
	// 消费方按仓过滤时天然排除空行（Where 命中条件），无需特判。
	TenantID  string    `gorm:"column:tenant_id;size:64;not null;default:default;index"`
	Platform  string    `gorm:"column:platform;size:16"`
	Owner     string    `gorm:"column:owner;size:128"`
	Repo      string    `gorm:"column:repo;size:128"`
	Type      string    `gorm:"column:type;size:32"`
	CreatedAt time.Time `gorm:"column:created_at;index"`
}

func (OutboxEvent) TableName() string { return "event_outbox" }

// AppendOutboxEvent 落一条事件（best-effort：失败由调用方告警，不阻断主流程）。
func (s *Store) AppendOutboxEvent(ctx context.Context, ev progress.Event) error {
	return appendOutboxEvent(s, ctx, ev)
}

// OutboxRow fanout 轮询行：游标 ID + 事件载荷。
type OutboxRow struct {
	ID    int64
	Event progress.Event
}

// ListOutboxEvents 游标分页拉取（id > afterID，按 id 升序，limit 收口）。
// 返回行数 < limit 表示本批拉完。
func (s *Store) ListOutboxEvents(ctx context.Context, afterID int64, limit int) ([]OutboxRow, error) {
	return listOutboxEvents(s, ctx, afterID, limit)
}

// MaxOutboxID 当前最大游标（fanout 启动基线——从"现在"开始，不重播历史）。
func (s *Store) MaxOutboxID(ctx context.Context) (int64, error) { return maxOutboxID(s, ctx) }

// PruneOutboxEvents 保留期清理（观测面数据，24h 足够跨实例 fanout 消化）。
func (s *Store) PruneOutboxEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	return pruneOutboxEvents(s, ctx, cutoff)
}

// PruneDeltaOutboxEvents agent.delta 行的短期清理（#67：delta 参与跨实例
// fanout 后体量远大于状态事件，而其价值只在流式窗口内——独立短保留期，防
// outbox 膨胀）。cutoff 之前的 agent.delta 行删除，其余类型不受影响。
func (s *Store) PruneDeltaOutboxEvents(ctx context.Context, cutoff time.Time) (int64, error) {
	return pruneDeltaOutboxEvents(s, ctx, cutoff)
}
