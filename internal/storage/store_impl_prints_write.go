// store_impl_prints_write.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"strconv"
	"time"

	"github.com/oagentkit/textx/reportutil"
	"github.com/yi-nology/argus/internal/domain"
	"github.com/yi-nology/argus/internal/learning"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// upsertPrint 的包级实现（转发壳见原文件）。
func upsertPrint(s *Store, ctx context.Context, platform, owner, repo, number, fingerprint string,
	f domain.Finding, taskID string, at time.Time) (suppressed bool, err error) {
	res := s.UpsertPrints(ctx, platform, owner, repo, number, taskID, at,
		[]domain.PrintUpsert{{FP: fingerprint, Finding: f}})
	if len(res) == 0 {
		return false, nil
	}
	return res[0].Suppressed, res[0].Err
}

// upsertPrints 的包级实现（转发壳见原文件）。
func upsertPrints(s *Store, ctx context.Context, platform, owner, repo, number, taskID string,
	at time.Time, items []domain.PrintUpsert) []domain.PrintUpsertResult {
	out := make([]domain.PrintUpsertResult, len(items))
	if len(items) == 0 {
		return out
	}
	_ = s.withWrite(func() error {
		for i, it := range items {
			it := it
			var prior *domain.PrintState
			var suppressed bool
			err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				p, sup, terr := s.upsertPrintTx(tx, platform, owner, repo, number, it.FP, it.Finding, taskID, at)
				prior, suppressed = p, sup
				return terr
			})
			out[i] = domain.PrintUpsertResult{Prior: prior, Suppressed: suppressed, Err: err}
		}
		return nil
	})
	return out
}

// upsertPrintTx 的包级实现（转发壳见原文件）。
func upsertPrintTx(s *Store, tx *gorm.DB, platform, owner, repo, number, fingerprint string,
	f domain.Finding, taskID string, at time.Time) (prior *domain.PrintState, suppressed bool, err error) {
	fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
	var p FindingPrint
	exact := false // 本轮是否经"完全同评语"通道并入（行号跟随用）
	pQ := tx.Where(
		"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
		platform, owner, repo, number, fingerprint)
	// MySQL/pg 下 FOR UPDATE：多副本共库时防止并发轮次的"读-改-全列回写"
	// 互相覆盖（抑制状态/TimesSeen 丢失）；SQLite 单写串行无需锁（v3.8.0）。
	if !s.isSQLite() {
		pQ = pQ.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err = pQ.First(&p).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, false, err
		}
		// 近重复匹配层（v3.7.0）：精确未命中时，同 PR 同文件内行号相容且
		// 评语近重复的既有条目视为同一问题——并入它（计数/抑制状态延续），
		// 新措辞记为别名。措辞漂移不再产生"永远新问题"。
		// v3.8.0：完全同评语免行距（exact 通道）——行号整体位移 >±2 时
		// 计数/抑制不失忆；并入后行号跟随新值，行内评论位置不漂移。
		near, xexact, nerr := findNearPrint(tx, platform, owner, repo, number, f)
		if nerr != nil {
			return nil, false, nerr
		}
		if near != nil {
			p = *near
			exact = xexact
			prior = printStateOf(&p)
			if fingerprint != p.Print {
				// 幂等写入：别名 PR 域化（v3.9.2）后同 (PR, alias_fp) 重复创建
				// 仍可能发生（同 PR 内措辞漂移回摆），冲突保留既有映射。
				if aerr := tx.Clauses(clause.OnConflict{DoNothing: true}).
					Create(&PrintAlias{Platform: platform, Owner: owner, Repo: repo, Number: number,
						AliasFP: fingerprint, PrintFP: p.Print, UpdatedAt: at}).Error; aerr != nil {
					return nil, false, aerr
				}
			}
		} else {
			// 全新指纹
			p = FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number, Print: fingerprint,
				File: f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment, Plugin: learning.FirstBy(f.By),
				Status:      PrintOpen,
				FirstTaskID: taskID, LastTaskID: taskID, TimesSeen: 1,
				FirstSeenAt: at, LastSeenAt: at,
			}
			return nil, false, tx.Create(&p).Error
		}
	} else {
		prior = printStateOf(&p)
	}
	// 既有指纹：更新可见性元数据；保留 suppressed 状态（误报反馈不被新轮次冲掉）
	p.LastTaskID = taskID
	p.LastSeenAt = at
	p.TimesSeen++
	p.Severity = reportutil.Higher(p.Severity, f.Severity)
	if p.Plugin == "" {
		p.Plugin = learning.FirstBy(f.By)
	}
	// 行号跟随：exact（完全同评语，含行号漂移）无条件跟随新值；
	// near（近重复）保持 ±2 相容才跟随的保守语义。
	if f.Line > 0 && (exact || absDiff(p.Line, f.Line) <= 2) {
		p.Line = f.Line
	}
	if err := tx.Save(&p).Error; err != nil {
		return prior, false, err
	}
	return prior, p.Status == PrintSuppressed, nil
}

// suppressPrint 的包级实现（转发壳见原文件）。
func suppressPrint(s *Store, ctx context.Context, platform, owner, repo, number, fingerprint string,
	f domain.Finding, at time.Time) (matched bool, err error) {
	err = s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
			var p FindingPrint
			err := tx.Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
				platform, owner, repo, number, fingerprint).First(&p).Error
			if err == nil {
				matched = true
				p.Status = PrintSuppressed
				p.LastSeenAt = at
				if f.File != "" {
					p.File, p.Line, p.Severity, p.Comment = f.File, f.Line, f.Severity, f.Comment
				}
				if err := tx.Save(&p).Error; err != nil {
					return err
				}
				return suppressFamily(tx, platform, owner, repo, number, at)
			}
			if err != gorm.ErrRecordNotFound {
				return err
			}
			p = FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number, Print: fingerprint,
				File: f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment,
				Status:      PrintSuppressed,
				FirstTaskID: "manual", LastTaskID: "manual", TimesSeen: 0,
				FirstSeenAt: at, LastSeenAt: at,
			}
			if err := tx.Create(&p).Error; err != nil {
				return err
			}
			return suppressFamily(tx, platform, owner, repo, number, at)
		})
	})
	return matched, err
}

// recordPrintRegret 的包级实现（转发壳见原文件）。
func recordPrintRegret(s *Store, ctx context.Context, platform, owner, repo, number, fingerprint string, f domain.Finding) error {
	return s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			fingerprint = resolveAliasFP(tx, platform, owner, repo, number, fingerprint)
			var p FindingPrint
			err := tx.Where(
				"platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
				platform, owner, repo, number, fingerprint).First(&p).Error
			if err != nil {
				if err != gorm.ErrRecordNotFound {
					return err
				}
				now := time.Now()
				return tx.Create(&FindingPrint{
					Platform: platform, Owner: owner, Repo: repo, Number: number,
					Print: fingerprint,
					File:  f.File, Line: f.Line, Severity: f.Severity, Comment: f.Comment,
					Plugin:      learning.FirstBy(f.By),
					Status:      PrintOpen,
					RegretCount: 1,
					FirstTaskID: "reflect", LastTaskID: "reflect", TimesSeen: 0,
					FirstSeenAt: now, LastSeenAt: now,
				}).Error
			}
			return tx.Model(&p).Updates(map[string]any{
				"regret_count": gorm.Expr("regret_count + 1"),
				"updated_at":   time.Now(),
			}).Error
		})
	})
}

// restorePrint 的包级实现（转发壳见原文件）。
func restorePrint(s *Store, ctx context.Context, platform, owner, repo string, number int, prefix string) (bool, error) {
	if len(prefix) < 6 || !validFPPrefix(prefix) {
		return false, nil
	}
	matched := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []FindingPrint
		// number 列是 text：绑参走 Itoa（pgx 严格类型拒 int→text 隐式转换——
		// pg 矩阵实锤 encode args[3]；MySQL/sqlite 宽松转换掩盖过此错位）。
		if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print LIKE ?",
			platform, owner, repo, strconv.Itoa(number), prefix+"%").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			// 别名前缀兜底：alias_fp 为全指纹，前缀命中歧义时同样不猜。
			// 别名表已 PR 域化（v3.9.2），直接按本 PR 过滤。
			var aliases []PrintAlias
			if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND alias_fp LIKE ?",
				platform, owner, repo, strconv.Itoa(number), prefix+"%").Find(&aliases).Error; err != nil {
				return err
			}
			if len(aliases) != 1 {
				return nil // 无匹配或歧义：不猜
			}
			if err := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND print = ?",
				platform, owner, repo, strconv.Itoa(number), aliases[0].PrintFP).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) != 1 {
				return nil
			}
		}
		if len(rows) != 1 {
			return nil // 无匹配或前缀歧义：不猜
		}
		matched = true
		if rows[0].Status == PrintSuppressed {
			rows[0].Status = PrintOpen
			if err := tx.Save(&rows[0]).Error; err != nil {
				return err
			}
		}
		// 家族对称（v3.133.1）：恢复与抑制同规则——同一问题的变体要么都在要么都
		// 不在，不留"恢复一条其余仍被压"的半边状态。
		return restoreFamily(tx, platform, owner, repo, strconv.Itoa(number))
	})
	return matched, err
}
