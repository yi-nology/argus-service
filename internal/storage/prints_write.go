// prints_write.go —— 意见指纹写侧：入库/批量入库、抑制与 regret/恢复。

package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/argus/internal/domain"
)

// UpsertPrint 实现 domain.PrintStore：同一指纹跨轮次 = 同一问题 → 更新状态而非新增。
// 行号 ±2 容差内采纳新行号（匹配辅助，不入指纹）。
func (s *Store) UpsertPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f domain.Finding, taskID string, at time.Time) (suppressed bool, err error) {
	return upsertPrint(s, ctx, platform, owner, repo, number, fingerprint, f, taskID, at)
}

// UpsertPrints 批量入库：整批共用一次进程内写锁（SQLite writeMu），避免多意见 PR
// 逐条抢锁；逐条独立事务，单条失败不拖垮整批（与单条 UpsertPrint 语义一致）。
// 返回与 items 等长；Prior 为入库前状态（Merger 读 TimesSeen/RegretCount/原文用）。
func (s *Store) UpsertPrints(ctx context.Context, platform, owner, repo, number, taskID string,
	at time.Time, items []domain.PrintUpsert) []domain.PrintUpsertResult {
	return upsertPrints(s, ctx, platform, owner, repo, number, taskID, at, items)
}

// upsertPrintTx 单条入库事务体（调用方持有 writeMu）。返回入库前状态与抑制标记。
func (s *Store) upsertPrintTx(tx *gorm.DB, platform, owner, repo, number, fingerprint string,
	f domain.Finding, taskID string, at time.Time) (prior *domain.PrintState, suppressed bool, err error) {
	return upsertPrintTx(s, tx, platform, owner, repo, number, fingerprint, f, taskID, at)
}

// printStateOf 行 → 读侧投影（入库前状态快照）。
func printStateOf(p *FindingPrint) *domain.PrintState {
	return &domain.PrintState{
		Status:      p.Status,
		Line:        p.Line,
		Severity:    p.Severity,
		TimesSeen:   p.TimesSeen,
		RegretCount: p.RegretCount,
		Comment:     p.Comment, // 入库原文（标注前）——pattern regret 记账键的权威来源
	}
}

// SuppressPrint 误报反馈（§3.3 反馈通道最小入口，v3.0.1）：
// 把指纹置为 suppressed（既有 → 更新并返回 matched=true；
// 不存在 → 以 suppressed 预插（意见可能尚未在库），matched=false）。
// 之后每轮 Merger 自动剔除该意见。
func (s *Store) SuppressPrint(ctx context.Context, platform, owner, repo, number, fingerprint string,
	f domain.Finding, at time.Time) (matched bool, err error) {
	return suppressPrint(s, ctx, platform, owner, repo, number, fingerprint, f, at)
}

// RecordPrintRegret R3.5 反思剔除某指纹时累加 regret 计数：该指纹历史上被
// 终审 rubric 判为误报的次数，跨轮由 Merger 读取并标注到报告（只标注，不自动
// suppress——持久抑制仍必须来自人工反馈通道，防自动化误杀）。
// 行不存在时以 f 的原始内容创建（v3.8.0：此前创建 File=""/Comment="" 的幽灵行，
// 近重复层永不回收、v2 迁移还会因同 PR 多条空行重算出同一常量指纹而撞唯一索引）。
// 指纹先经别名解析：剔除的可能以别名身份在库。
func (s *Store) RecordPrintRegret(ctx context.Context, platform, owner, repo, number, fingerprint string, f domain.Finding) error {
	return recordPrintRegret(s, ctx, platform, owner, repo, number, fingerprint, f)
}

// RestorePrint 恢复被误抑制的意见（suppress 反向操作）：fp 短码前缀解析后置回 open，
// 下一轮审查起重新可见。只作用于 suppressed 行——已 open 的同前缀行视为已恢复
// （matched=true 不改状态）；无匹配或前缀歧义 matched=false，由调用方回帖提示。
// restore 不回流知识：恢复只是撤销剔除，不是新的误报/正确结论。
// 别名回退（v3.8.0）：suppress 可经别名表进来（措辞漂移后报告展示的是别名短码），
// restore 同样必须走别名兜底——否则抑制是"进得来、出不去"的单向门。
func (s *Store) RestorePrint(ctx context.Context, platform, owner, repo string, number int, prefix string) (bool, error) {
	return restorePrint(s, ctx, platform, owner, repo, number, prefix)
}
