// printalias_migrate.go —— 指纹 v2 与 print_aliases PR 域的存量迁移。

package storage

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/yi-nology/argus/internal/domain"
)

// migratePrintFingerprintsV2 存量迁移（v3.7.0；v3.8.0 起由 SchemaMigration 哨兵
// 门控，只跑一次不再每次启动全表扫）：v1 指纹 = sha256(file|规范化comment)
// （无行号、无版本域），v2 加入行号与版本域。
// 同 PR 内重算后指纹相同的行（v1 时代 regret 幽灵行/重复手工行的遗留）先按组合并：
// TimesSeen/RegretCount 取 max、抑制状态 OR、时间取早/晚、非空内容保留——否则
// 第二行 UPDATE 必撞 idx_print 唯一索引，迁移每次启动在同一行失败、永不完成
// （v3.8.0 修复）。合并后逐行回写；抑制状态、复现/regret 计数原地保留。
// 迁移后旧短码不再可解析（CHANGELOG 有告知）；单行失败不阻断启动，留待重试。
func (s *Store) migratePrintFingerprintsV2(ctx context.Context) (migrated int, err error) {
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

// migratePrintAliasPRScope print_aliases 表重建（v3.9.2，SchemaMigration 哨兵
// 门控）：PR 四列并入主键。旧形态 alias_fp 全局唯一主键与"指纹按 PR 归属"矛盾
// ——两个 PR 同款措辞漂移时第二个 PR 建别名撞主键、UpsertPrint 整体回滚。
// 回填规则：旧行按 print_fp 关联 finding_prints 反查 PR——命中多个 PR 逐 PR
// 复制（"alias_fp ↔ 该 PR 的规范行"在各 PR 内均成立），零命中（规范行已被
// 清理的孤儿别名）丢弃。新库 AutoMigrate 即新形态，重建空表无害（幂等）。
// 语句全部为内联字面量，无运行期插值——迁移 DDL 不含任何用户输入。
func (s *Store) migratePrintAliasPRScope(ctx context.Context) (int, error) {
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
