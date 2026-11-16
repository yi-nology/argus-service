// store_impl_printalias.go —— Store 方法体的包级实现（转发壳见原文件）。

package storage

import (
	"context"
	"time"

	"github.com/yi-nology/argus/internal/domain"
	"gorm.io/gorm"
)

// suppressPrintNear 的包级实现（转发壳见原文件）。
func suppressPrintNear(s *Store, ctx context.Context, platform, owner, repo, number, file, comment string, at time.Time) (fp string, matched bool, err error) {
	err = s.withWrite(func() error {
		return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var rows []FindingPrint
			q := tx.Where("platform = ? AND owner = ? AND repo = ? AND number = ? AND file = ?", platform, owner, repo, number, file)
			var best *FindingPrint
			if err := q.Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
				return err
			}
			for i := range rows {
				if commentNear(rows[i].Comment, comment) && (best == nil || rows[i].TimesSeen > best.TimesSeen) {
					best = &rows[i]
				}
			}
			if best != nil {
				best.Status = PrintSuppressed
				best.LastSeenAt = at
				matched = true
				fp = best.Print
				if err := tx.Save(best).Error; err != nil {
					return err
				}
				return suppressFamily(tx, platform, owner, repo, number, at)
			}
			fp = domain.Fingerprint(file, comment, 0)
			p := FindingPrint{
				Platform: platform, Owner: owner, Repo: repo, Number: number,
				Print: fp,
				File:  file, Comment: comment, Status: PrintSuppressed,
				FirstTaskID: "manual", LastTaskID: "manual", TimesSeen: 0,
				FirstSeenAt: at, LastSeenAt: at,
			}
			if err := tx.Create(&p).Error; err != nil {
				return err
			}
			return suppressFamily(tx, platform, owner, repo, number, at)
		})
	})
	return fp, matched, err
}

// migratePrintFingerprintsV2 的包级实现（转发壳见原文件）。
func migratePrintFingerprintsV2(s *Store, ctx context.Context) (migrated int, err error) {
	var rows []FindingPrint
	if err := s.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return 0, err
	}
	type groupKey struct{ pr, nv string }
	groups := map[groupKey][]FindingPrint{}
	order := []groupKey{}
	for _, r := range rows {
		nv := domain.Fingerprint(r.File, r.Comment, r.Line)
		k := groupKey{r.Platform + "/" + r.Owner + "/" + r.Repo + "/" + r.Number, nv}
		if _, seen := groups[k]; !seen {
			order = append(order, k)
		}
		groups[k] = append(groups[k], r)
	}
	for _, k := range order {
		g := groups[k]
		if len(g) == 1 {
			continue // 无碰撞，走普通逐行迁移
		}
		keep := g[0] // ID 最小（最老）者为主行
		for _, r := range g[1:] {
			if r.ID < keep.ID {
				keep = r
			}
		}
		for _, r := range g {
			if r.ID == keep.ID {
				continue
			}
			if r.TimesSeen > keep.TimesSeen {
				keep.TimesSeen = r.TimesSeen
			}
			if r.RegretCount > keep.RegretCount {
				keep.RegretCount = r.RegretCount
			}
			if r.Status == PrintSuppressed {
				keep.Status = PrintSuppressed
			}
			if r.FirstSeenAt.Before(keep.FirstSeenAt) {
				keep.FirstSeenAt = r.FirstSeenAt
			}
			if r.LastSeenAt.After(keep.LastSeenAt) {
				keep.LastSeenAt = r.LastSeenAt
			}
			if keep.File == "" {
				keep.File = r.File
			}
			if keep.Comment == "" {
				keep.Comment = r.Comment
			}
			if keep.Plugin == "" {
				keep.Plugin = r.Plugin
			}
		}
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			keep.Print = k.nv
			keep.UpdatedAt = time.Now()
			if err := tx.Save(&keep).Error; err != nil {
				return err
			}
			ids := make([]uint, 0, len(g)-1)
			for _, r := range g {
				if r.ID != keep.ID {
					ids = append(ids, r.ID)
				}
			}
			return tx.Where("id IN ?", ids).Delete(&FindingPrint{}).Error
		}); err != nil {
			return migrated, err
		}
		migrated += len(g) - 1
	}
	for _, r := range rows {
		nv := domain.Fingerprint(r.File, r.Comment, r.Line)
		if nv == r.Print {
			continue
		}
		res := s.db.WithContext(ctx).Model(&FindingPrint{}).Where("id = ?", r.ID).
			Update("print", nv)
		if res.Error != nil {
			return migrated, res.Error
		}
		migrated += int(res.RowsAffected)
	}
	return migrated, nil
}

// migratePrintAliasPRScope 的包级实现（转发壳见原文件）。
func migratePrintAliasPRScope(s *Store, ctx context.Context) (int, error) {
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.isSQLite() {
			if err := tx.Exec(`CREATE TABLE "print_aliases_new" ("platform" text NOT NULL, "owner" text NOT NULL, "repo" text NOT NULL, "number" text NOT NULL, "alias_fp" text NOT NULL, "print_fp" text, "updated_at" datetime, PRIMARY KEY ("platform", "owner", "repo", "number", "alias_fp"))`).Error; err != nil {
				return err
			}
		} else if s.isMySQL() {
			// MySQL 反引号表名与引号列名在 Go 源码里无需转义
			if err := tx.Exec("CREATE TABLE `print_aliases_new` (`platform` varchar(16) NOT NULL, `owner` varchar(128) NOT NULL, `repo` varchar(128) NOT NULL, `number` varchar(16) NOT NULL, `alias_fp` varchar(64) NOT NULL, `print_fp` varchar(64) DEFAULT NULL, `updated_at` datetime(3) DEFAULT NULL, PRIMARY KEY (`platform`, `owner`, `repo`, `number`, `alias_fp`)) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4").Error; err != nil {
				return err
			}
		} else {
			// PostgreSQL（v5.4.8）：双引号标识符 + text/timestamp 形态
			if err := tx.Exec(`CREATE TABLE print_aliases_new ("platform" text NOT NULL, "owner" text NOT NULL, "repo" text NOT NULL, "number" text NOT NULL, "alias_fp" text NOT NULL, "print_fp" text, "updated_at" timestamptz, PRIMARY KEY ("platform", "owner", "repo", "number", "alias_fp"))`).Error; err != nil {
				return err
			}
		}
		// 回填按 print_fp 反查 PR：多 PR 命中经 JOIN 逐 PR 复制，同 (PR, alias_fp)
		// 天然去重；updated_at 取 MAX（旧行 alias_fp 全局唯一，无并列）。
		if err := tx.Exec(`INSERT INTO print_aliases_new (platform, owner, repo, number, alias_fp, print_fp, updated_at) SELECT p.platform, p.owner, p.repo, p.number, a.alias_fp, a.print_fp, MAX(a.updated_at) FROM print_aliases a JOIN finding_prints p ON p.print = a.print_fp GROUP BY p.platform, p.owner, p.repo, p.number, a.alias_fp, a.print_fp`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DROP TABLE print_aliases`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`ALTER TABLE print_aliases_new RENAME TO print_aliases`).Error; err != nil {
			return err
		}
		return tx.Exec(`CREATE INDEX idx_print_aliases_print_fp ON print_aliases (print_fp)`).Error
	}); err != nil {
		return 0, err
	}
	var n int64
	if err := s.db.WithContext(ctx).Table("print_aliases").Count(&n).Error; err != nil {
		return 0, err
	}
	return int(n), nil
}
