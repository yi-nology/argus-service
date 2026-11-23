// tenant_callbacks.go —— 租户谓词/回填的 GORM 回调注册（收编：判据本体已下沉
// 上游 gormx v0.15.0 子包 tenant，oekit/oekit#225；本文件只剩装配一行 +
// 语义注释单源）。
//
// 机制（上游实现）：Query/Update/Delete 在表模型带 tenant_id 列时无条件
// 注入谓词（tenant.FromContext(ctx)，无声明 = Default）；表带 deleted_at
// 列且 ctx 未 WithDeleted → 同注入 deleted_at IS NULL；Create 在 tenant_id
// 零值时从 ctx 回填（显式写入优先）。逃生门 WithoutScope/WithDeleted；裸
// SQL（Raw/Exec 不经过回调）两路绕过都必须在注释里写明理由——裸 SQL 站点的
// 租户条件须手工拼。注册 fail-loud（重名/重复注册即报错）。

package storage

import (
	"fmt"

	"gorm.io/gorm"

	gtenant "github.com/oekit/gormx/tenant"
)

// registerTenantCallbacks 在 Open 时注册到本 Store 的 *gorm.DB（每库一次）。
// 注册失败向上报错——回调缺失 = 租户隔离静默失效，必须 fail-loud。
func registerTenantCallbacks(db *gorm.DB) error {
	if err := gtenant.Register(db, gtenant.WithCallbackPrefix("argus:tenant")); err != nil {
		return fmt.Errorf("storage: 注册租户回调失败: %w", err)
	}
	return nil
}
