// printalias_migrate.go —— 指纹 v2 与 print_aliases PR 域的存量迁移。

package storage

import (
	"context"
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
	return migratePrintFingerprintsV2(s, ctx)
}

// migratePrintAliasPRScope print_aliases 表重建（v3.9.2，SchemaMigration 哨兵
// 门控）：PR 四列并入主键。旧形态 alias_fp 全局唯一主键与"指纹按 PR 归属"矛盾
// ——两个 PR 同款措辞漂移时第二个 PR 建别名撞主键、UpsertPrint 整体回滚。
// 回填规则：旧行按 print_fp 关联 finding_prints 反查 PR——命中多个 PR 逐 PR
// 复制（"alias_fp ↔ 该 PR 的规范行"在各 PR 内均成立），零命中（规范行已被
// 清理的孤儿别名）丢弃。新库 AutoMigrate 即新形态，重建空表无害（幂等）。
// 语句全部为内联字面量，无运行期插值——迁移 DDL 不含任何用户输入。
func (s *Store) migratePrintAliasPRScope(ctx context.Context) (int, error) {
	return migratePrintAliasPRScope(s, ctx)
}
