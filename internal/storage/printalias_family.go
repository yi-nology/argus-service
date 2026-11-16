// printalias_family.go —— 别名抑制写路径：近重复抑制、抑制家族传播与恢复。

package storage

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// SuppressPrintNear 抑制通道的 file+原文 形态（API 请求无行号，无法直算 v2 指纹）：
// 先在同 PR 同文件内近重复检索既有条目并抑制其规范指纹（matched=true）；
// 未命中则以 line=0 的 v2 指纹预插 suppressed 行（matched=false）——真实意见
// 下轮入库时会经近重复层并回该行，抑制随之生效。
func (s *Store) SuppressPrintNear(ctx context.Context, platform, owner, repo, number, file, comment string, at time.Time) (fp string, matched bool, err error) {
	return suppressPrintNear(s, ctx, platform, owner, repo, number, file, comment, at)
}

// familyRelated 家族传播判定（**有方向**，v3.133.2）：anchor=已抑制行，
// cand=候选行。双侧带行号锚点走 0.4+行距±2（同函数不同问题的 0.364 反例被挡）；
// 锚点精确、候选 line=0 走 0.2+文件组件相交（模糊聚合形态归族）；**line=0 的
// 模糊行不得向 line>0 的精确行传播**——大杂烩行同时含多问题文本，是天然的
// 「桥」（实测 blob-vs-SQL=0.217 过 0.2），无向传播会连坐真意见（v3.133.2
// 活体对照抓的过度抑制）。模糊行之间仍按 0.2 归族。
func familyRelated(anchor, cand FindingPrint) bool {
	switch {
	case anchor.Line > 0 && cand.Line > 0:
		return linesClose(anchor.Line, cand.Line) && commentNearLoose(anchor.Comment, cand.Comment)
	case anchor.Line == 0 && cand.Line > 0:
		return false // 模糊锚不外溢到精确行（防大杂烩桥接连坐）
	default: // 候选为 line=0 模糊形态
		return fileSetIntersect(anchor.File, cand.File) && commentNearVague(anchor.Comment, cand.Comment)
	}
}

// suppressFamily 抑制家族传播（v3.133.1）：人工拒绝一条 = 拒绝同一问题的全部
// 措辞变体——从 suppressed 行出发不动点扩张，家族行并入抑制。此前须逐变体打
// 地鼠（实测同一问题四轮四种措辞，压二缺一即复活）。
func suppressFamily(tx *gorm.DB, platform, owner, repo, number string, at time.Time) error {
	for range 8 { // 家族规模小，不动点 8 轮封顶
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		grew := false
		for i := range rows {
			if rows[i].Status != PrintSuppressed {
				continue
			}
			for j := range rows {
				if rows[j].Status == PrintSuppressed {
					continue
				}
				if familyRelated(rows[i], rows[j]) {
					rows[j].Status = PrintSuppressed
					rows[j].LastSeenAt = at
					if err := tx.Save(&rows[j]).Error; err != nil {
						return err
					}
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
	return nil
}

// restoreFamily 恢复家族传播（suppressFamily 对称）：从 open 行出发把家族内的
// suppressed 行放回 open。
func restoreFamily(tx *gorm.DB, platform, owner, repo, number string) error {
	for range 8 {
		var rows []FindingPrint
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ?",
			platform, owner, repo, number).Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
			return err
		}
		grew := false
		for i := range rows {
			if rows[i].Status != PrintOpen {
				continue
			}
			for j := range rows {
				if rows[j].Status != PrintSuppressed {
					continue
				}
				if familyRelated(rows[i], rows[j]) {
					rows[j].Status = PrintOpen
					if err := tx.Save(&rows[j]).Error; err != nil {
						return err
					}
					grew = true
				}
			}
		}
		if !grew {
			return nil
		}
	}
	return nil
}
