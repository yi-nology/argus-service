// store_prune.go —— 保留期清理与用量/统计聚合面：Prune 族 + 月度 token + /stats/overview 数据源。

package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yi-nology/argus/internal/tenant"
)

// PruneTasks 保留策略第一阶（#109a 软删除）：created_at 早于 cutoff 的已终结
// 任务（done/failed/stale；pending/running 不动）**置 deleted_at**（并清 event_id
// 释放幂等唯一键），grace 窗口内可恢复（RestoreTask）、到期由 PruneTasksHard
// 物理清除。分批（每批 500）——单条大 UPDATE 长事务占住 SQLite 单写连接，
// 队头阻塞心跳（深挖修复，硬删时代同因）。返回本阶标记行数。
func (s *Store) PruneTasks(ctx context.Context, cutoff time.Time) (total int64, err error) {
	return pruneTasks(s, ctx, cutoff)
}

// PruneTasksHard 保留策略第二阶（#109a）：软删 grace 窗口（软删时刻早于
// graceCutoff）的任务行物理删除。调用方给 graceCutoff = retention + 宽限
// （bootstrap 以 7 天宽限接线：「删除后 7 天可恢复」合同口径）。返回物理删除行数。
func (s *Store) PruneTasksHard(ctx context.Context, graceCutoff time.Time) (total int64, err error) {
	return pruneTasksHard(s, ctx, graceCutoff)
}

// RestoreTask 恢复软删除任务（#109a「删除后 N 天可恢复」的兑现面）：清
// deleted_at。event_id 已在软删时释放（重投递重建语义优先于 delivery 去重），
// 恢复后同 delivery 重投不会被误判幂等。须 tenant.WithDeleted(ctx) 才能命中
// 软删行（谓词回调默认排除）。返回是否命中（未软删/不存在 = false）。
func (s *Store) RestoreTask(ctx context.Context, id string) (bool, error) {
	ctx = tenant.WithDeleted(ctx)
	res := s.db.WithContext(ctx).Model(&Task{}).
		Where("id = ? AND deleted_at IS NOT NULL", id).
		Update("deleted_at", nil)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// PruneQASessions 会话保留策略：updated_at 早于 cutoff 的问答会话清理（深挖修复：
// qa_sessions 此前永不清理，history_json 持续追加单调膨胀）。分批删除（每批 500，
// 与 PruneTasks 同理：单条大 DELETE 长事务占住 SQLite 单写连接，v3.9.1 补齐）。
// 返回删除行数。
func (s *Store) PruneQASessions(ctx context.Context, cutoff time.Time) (total int64, err error) {
	return pruneQASessions(s, ctx, cutoff)
}

// PruneStalePrints 指纹保留策略：LastSeenAt 早于 cutoff 的指纹清理（深挖修复：
// finding_prints 含 LONGTEXT comment 永不清理）。语义安全：长期未复现的指纹
// 遗忘后最坏效果是"同型意见重新从头计数"，不会复活已抑制意见（suppressed
// 状态随行删除，同型意见若真复发会以 open 新行参与——保留期给足即可）。
// v3.8.0：随行清理指向已删指纹的孤儿别名（print_aliases 此前无限增长）。
// v3.9.1：分批删除（每批 500），别名清理跟随本批指纹。
func (s *Store) PruneStalePrints(ctx context.Context, cutoff time.Time) (total int64, err error) {
	return pruneStalePrints(s, ctx, cutoff)
}

// RepoTokenUsage 仓库维度 token 用量聚合（月度预算/用量报表，§5 v3.1）。
type RepoTokenUsage struct {
	Platform string `json:"platform"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	Tokens   int64  `json:"tokens"`
	Tasks    int64  `json:"tasks"`
}

// RepoMonthlyTokens 单仓库自 since 起的 token 消耗（done 任务口径——failed 任务的
// 中途消耗不计入预算，避免平台故障误烧预算）。
func (s *Store) RepoMonthlyTokens(ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	return repoMonthlyTokens(s, ctx, platform, owner, repo, since)
}

// MonthlyTokensByRepo 全部仓库自 since 起的 token 用量（GET /usage/monthly）。
func (s *Store) MonthlyTokensByRepo(ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	return monthlyTokensByRepo(s, ctx, since)
}

// RepoMonthlyTokensReal 真实消费账（v6.9.0 P1-06 #11 → #32 升级为逐调用实销）：
// 口径从「任务终态聚合」改为 usage_event 逐调用记录（status=settled）——运行中
// 任务的调用即时入账、重试各次调用全部计入（终态聚合会把重试覆盖成末次
// attempt 的 tokens_used），多副本不再等终态才有真实消费可见。配额账
// （RepoMonthlyTokens，done 口径）语义不回退：平台故障误烧不占配额；真实
// 消费账独立上限（ARGUS_REAL_TOKEN_BUDGET）。历史任务经终态 delta 兜底 +
// 一次性回填迁移补齐（backfill* 事件同为 settled，天然并入本口径）。
func (s *Store) RepoMonthlyTokensReal(ctx context.Context, platform, owner, repo string, since time.Time) (int64, error) {
	return repoMonthlyTokensReal(s, ctx, platform, owner, repo, since)
}

// MonthlyTokensByRepoReal 全部仓库实销聚合（/usage/monthly 双账本展示）。
// tasks 列 = 触达的不同任务数（同任务多笔调用去重，列义与配额账保持一致）。
func (s *Store) MonthlyTokensByRepoReal(ctx context.Context, since time.Time) ([]RepoTokenUsage, error) {
	return monthlyTokensByRepoReal(s, ctx, since)
}

// PruneUsageEvents 账本保留清理（#32）：usage_event 按 occurred_at、已收口
// 预留（settled/released）按 updated_at 早于 cutoff 分批删除（每批 500，与
// PruneTasks 同理——单条大 DELETE 长事务占住 SQLite 单写连接队头阻塞心跳）。
// 活跃预留（reserved）不动：仍在占额度判定（过期的由 ReleaseExpiredReservations
// 先置 released，下轮随龄出局）。与 PruneTasks 同 cutoff 时序安全：任务保留
// ≥ 事件龄（事件 occurred_at ≥ 任务 created_at），任务未清则其事件必在——
// 不存在「任务还在、事件被清 → 启动对账重复回填」的窗口。
func (s *Store) PruneUsageEvents(ctx context.Context, cutoff time.Time) (total int64, err error) {
	return pruneUsageEvents(s, ctx, cutoff)
}

// jsonUnmarshal 是 encoding/json.Unmarshal 的包内别名，便于测试 mock。
var jsonUnmarshal = jsonUnmarshalFn

func jsonUnmarshalFn(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// RepoStats 仓库维度审查概览聚合行（GET /stats/overview，v3.88.0）。
type RepoStats struct {
	Platform   string `json:"platform"`
	Owner      string `json:"owner"`
	Repo       string `json:"repo"`
	TasksDone  int64  `json:"tasks_done"`
	Approved   int64  `json:"verdict_approve"`
	Commented  int64  `json:"verdict_comment"`
	Rejected   int64  `json:"verdict_request_changes"`
	Tokens     int64  `json:"tokens_used"`
	Suppressed int64  `json:"findings_suppressed"`
	// 回复控制生效面（v8.5.0）：窗口内节流跳过回帖的任务数 + 按仓设置静默
	// 的意见条数（floor=低于级别地板 / cap=超出数量上限）。
	ReplyThrottled int64 `json:"reply_throttled"`
	SilencedFloor  int64 `json:"silenced_floor"`
	SilencedCap    int64 `json:"silenced_cap"`
}

// StatsOverview 窗口内按仓聚合任务量 / verdict 分布 / token 用量 / 抑制计数。
// tasks 与 prints 两表各自 GROUP BY 后在 Go 侧合并——抑制计数口径 = 窗口内
// 首次出现的意见中当前处于 suppressed 状态的数量（抑制时刻不入库，以首次
// 出现时间近似）。verdict 分布在 SQL 层用 CASE WHEN（SQLite/MySQL 通用）。
func (s *Store) StatsOverview(ctx context.Context, since time.Time, platform, owner, repo string) ([]RepoStats, error) {
	return statsOverview(s, ctx, since, platform, owner, repo)
}
