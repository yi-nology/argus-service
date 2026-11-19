// sequence_fix.go —— PG 自增序列对齐（#生产实锤：dbcopy 显式 ID 灌库后
// 序列停在低位，新插入永久 23505——finding_prints 自 2026-11-05 dbcopy 起
// 静默两周，新指纹记账/抑制学习全失忆，靠"撞墙烧 seq"理论要烧过 max(id)
// 才自愈，低流量表永远等不到）。
//
// 语义：对每个 serial/identity 序列，next value <= max(col) 即 setval(max+1)。
// 幂等、无副作用（序列前进是安全的；不动数据行）。SQLite 无需处理（rowid
// 算法按 max+1 自取）；MySQL AUTO_INCREMENT 在插入撞已有 id 时同样会失败，
// 但 MySQL 历史迁移未用 dbcopy 形态，暂不覆盖（如遇同类问题按同一思路补）。

package storage

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RepairSequencesDB 对给定 gorm 连接（须为 PG）做序列对齐，返回被修复的
// 表清单（空 = 健康）。供 storage.Store 与 dbcopy 等裸连接调用方共用。
func RepairSequencesDB(ctx context.Context, db *gorm.DB) ([]string, error) {
	type seqPair struct {
		Seq   string
		Table string
		Col   string
	}
	var pairs []seqPair
	if err := db.WithContext(ctx).Raw(`
		SELECT s.relname AS seq, t.relname AS "table", a.attname AS col
		FROM pg_class s
		JOIN pg_depend d ON d.objid = s.oid AND d.deptype = 'a'
		JOIN pg_class t ON t.oid = d.refobjid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
		WHERE s.relkind = 'S'`).Scan(&pairs).Error; err != nil {
		return nil, fmt.Errorf("storage: 枚举序列失败: %w", err)
	}
	var repaired []string
	for _, p := range pairs {
		// 序列当前 next 值（is_called=false 时 last_value 即下一个待发值）。
		var lastValue int64
		var isCalled bool
		seqIdent := pgQuoteIdent(p.Seq)
		if err := db.WithContext(ctx).Raw(
			fmt.Sprintf(`SELECT last_value, is_called FROM %s`, seqIdent)).
			Row().Scan(&lastValue, &isCalled); err != nil {
			return repaired, fmt.Errorf("storage: 读序列 %s 失败: %w", p.Seq, err)
		}
		next := lastValue
		if isCalled {
			next = lastValue + 1
		}
		// 表当前最大 id（空表 0 → 无需修复）。
		var maxID int64
		if err := db.WithContext(ctx).Raw(
			fmt.Sprintf(`SELECT COALESCE(MAX(%s), 0) FROM %s`,
				pgQuoteIdent(p.Col), pgQuoteIdent(p.Table))).
			Row().Scan(&maxID); err != nil {
			return repaired, fmt.Errorf("storage: 读 %s.%s 最大值失败: %w", p.Table, p.Col, err)
		}
		if maxID == 0 || next > maxID {
			continue
		}
		// setval(max+1, false)：下一个 nextval 即 max+1（is_called=false 语义）。
		if err := db.WithContext(ctx).Exec(
			fmt.Sprintf(`SELECT setval(%s, %d, false)`, seqIdent, maxID+1)).Error; err != nil {
			return repaired, fmt.Errorf("storage: setval %s 失败: %w", p.Seq, err)
		}
		repaired = append(repaired, p.Table)
	}
	return repaired, nil
}

// pgQuoteIdent 双引号钉标识符（来自 pg_catalog，天然可信；仍按 PG 规则转义
// 内嵌引号防拼 SQL 面意外）。
func pgQuoteIdent(id string) string {
	quoted := `"`
	for _, r := range id {
		if r == '"' {
			quoted += `""`
			continue
		}
		quoted += string(r)
	}
	return quoted + `"`
}

// RepairSequences Store 方法：仅 PG 方言执行（SQLite/MySQL 语义不需要，返回空）。
// bootstrap 启动期调用（修复即 WARN 留痕）；dbcopy 迁移完成后直接调 RepairSequencesDB。
func (s *Store) RepairSequences(ctx context.Context) ([]string, error) {
	if s.dialect != DialectPostgres {
		return nil, nil
	}
	return RepairSequencesDB(ctx, s.db)
}