// store_tenant_purge.go —— PurgeTenant 门面：租户级级联清理（#109a）。

package storage

import "context"

// PurgeTenant 租户级级联清理（#109a 验收面：「租户退订、法务留存」的兑现工具）：
// 物理删除目标租户在全部租户业务表（purgeTargets，与 tenantBackfillTables 同源）
// 中的行——含软删除行（退约 = 立即清除，软删 grace 恢复窗口不适用）。
// dry-run=true 只返回各表命中计数不落刀（运维先预估后执行纪律）。
// 返回 map[表名]行数（只含有行表）；调用方负责审计留痕（CLI 面已打结构化输出）。
//
// 谓词语义：裸 Table 查询绕过租户/软删回调，Where 只认本函数 tenant_id 条件——
// 绝不能用 Model 形态（回调会把 ctx 租户拼进来，跨租户清理静默失效）。
func (s *Store) PurgeTenant(ctx context.Context, tenantID string, dryRun bool) (map[string]int64, error) {
	return purgeTenant(s, ctx, tenantID, dryRun)
}
