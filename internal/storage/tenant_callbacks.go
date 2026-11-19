// tenant_callbacks.go —— 租户谓词/回填的 GORM 回调注册（#87/#94 机制面）。
//
// 三条回调把租户隔离从「每个端点自觉加 where」变成存储层机制：
//   - Query/Update/Delete：Statement 表模型带 tenant_id 列时无条件注入谓词
//     （tenant.FromContext(ctx)，无声明 = Default）；
//   - Create：tenant_id 为零值时从 ctx 回填。
// 显式逃生门 tenant.WithoutScope(ctx)（控制面跨租户运维）与裸 SQL（Raw/Exec
// 不经过回调）两路绕过都必须在注释里写明理由——裸 SQL 站点的租户条件须手工拼。

package storage

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/yi-nology/argus/internal/tenant"
)

// registerTenantCallbacks 在 Open 时注册到本 Store 的 *gorm.DB（每库一次）。
// 注册失败（重名等）向上报错——回调缺失 = 租户隔离静默失效，必须 fail-loud。
func registerTenantCallbacks(db *gorm.DB) error {
	scope := func(tx *gorm.DB) {
		if tenant.ScopeSkipped(tx.Statement.Context) {
			return
		}
		if tx.Statement.Schema == nil {
			return // 裸 Table() 形态：无模型 schema 可判，跳过（此类站点须手工拼条件）
		}
		if tx.Statement.Schema.LookUpField("tenant_id") == nil {
			return // 全局基础设施表（leader_leases/schema_migrations）：无租户列
		}
		tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
			clause.Eq{Column: clause.Column{Name: "tenant_id"}, Value: tenant.FromContext(tx.Statement.Context)},
		}})
		// #109a 软删除机制面：表带 deleted_at 列且 ctx 未显式 WithDeleted →
		// 注入 deleted_at IS NULL（与租户谓词同一强制注入形态；逃生门
		// tenant.WithDeleted 供清理/恢复/取证面使用）。无该列的表不受影响。
		if tx.Statement.Schema.LookUpField("deleted_at") != nil && !tenant.DeletedIncluded(tx.Statement.Context) {
			tx.Statement.AddClause(clause.Where{Exprs: []clause.Expression{
				clause.Eq{Column: clause.Column{Name: "deleted_at"}, Value: nil},
			}})
		}
	}
	if err := db.Callback().Query().Before("gorm:query").Register("argus:tenant_scope", scope); err != nil {
		return fmt.Errorf("storage: 注册租户 Query 回调失败: %w", err)
	}
	if err := db.Callback().Update().Before("gorm:update").Register("argus:tenant_scope", scope); err != nil {
		return fmt.Errorf("storage: 注册租户 Update 回调失败: %w", err)
	}
	if err := db.Callback().Delete().Before("gorm:delete").Register("argus:tenant_scope", scope); err != nil {
		return fmt.Errorf("storage: 注册租户 Delete 回调失败: %w", err)
	}

	setTenant := func(tx *gorm.DB) {
		if tx.Statement.Schema == nil {
			return
		}
		f := tx.Statement.Schema.LookUpField("tenant_id")
		if f == nil {
			return
		}
		if v, zero := f.ValueOf(tx.Statement.Context, tx.Statement.ReflectValue); !zero {
			if s, ok := v.(string); ok && s != "" {
				return // 显式写入租户优先（跨租户搬运等高级场景）
			}
		}
		tx.Statement.SetColumn("tenant_id", tenant.FromContext(tx.Statement.Context))
	}
	if err := db.Callback().Create().Before("gorm:create").Register("argus:tenant_set", setTenant); err != nil {
		return fmt.Errorf("storage: 注册租户 Create 回调失败: %w", err)
	}
	return nil
}
