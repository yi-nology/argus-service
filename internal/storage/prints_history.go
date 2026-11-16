// prints_history.go —— 历史意见检索（v3.138.0，MCP search_findings 数据面）：
// 跨轮次/跨任务检索本仓已见意见（prints 表是意见的持久身份账本）——「这个问题
// 以前报过吗/怎么裁的/被压过吗」的确定性入口。与知识库（search_knowledge，
// 组织级规范/结论）分工：这里查的是**本仓的意见流水账**。

package storage

import (
	"context"
	"time"
)

// FindingHistoryItem 历史意见检索结果。
type FindingHistoryItem struct {
	FP        string    `json:"fp"` // 完整指纹（短码前缀可直接用于 suppress/restore）
	File      string    `json:"file"`
	Line      int       `json:"line"`
	Severity  string    `json:"severity"`
	Comment   string    `json:"comment"`
	Status    string    `json:"status"` // open | suppressed
	TimesSeen int       `json:"times_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// SearchFindingsHistory 本仓历史意见检索：query 对评语/文件路径做 LIKE 匹配
// （参数化，无注入面；含 % 时按通配语义），last_seen 倒序。limit 钳 1..50
// （缺省 10）。空 query = 最近意见流水。
func (s *Store) SearchFindingsHistory(ctx context.Context, platform, owner, repo, query string, limit int) ([]FindingHistoryItem, error) {
	return searchFindingsHistory(s, ctx, platform, owner, repo, query, limit)
}
