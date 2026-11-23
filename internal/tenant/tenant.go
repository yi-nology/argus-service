// Package tenant —— gormx/tenant 的 argus 门面（收编：实现已下沉上游 gormx
// v0.15.0 子包 tenant，oekit/oekit#225；本包保留消费方 import 单点 + 仓内
// 特有的 Scoped 逐查询形态——上游只有 callback 强制注入面）。
//
// 语义逐字委托：租户归一/ctx 贯穿/逃生门（WithoutScope/WithDeleted）全在
// 上游；GORM 强制谓词回调注册在 storage.Open 经 tenant.Register（原
// registerTenantCallbacks 收编）。
package tenant

import (
	"context"

	gorm "gorm.io/gorm"

	gtenant "github.com/oekit/gormx/tenant"
)

// Default 缺省租户（未声明主体归一入此桶）。
const Default = gtenant.Default

// Normalize 租户 ID 归一（空/空白 → Default）。
var Normalize = gtenant.Normalize

// NewContext 把租户写入 ctx（写入前归一）。
var NewContext = gtenant.NewContext

// FromContext 从 ctx 取租户 ID；未写入 → Default。
var FromContext = gtenant.FromContext

// WithoutScope 标记 ctx 跳过租户谓词注入（跨租户运维逃生门）。
var WithoutScope = gtenant.WithoutScope

// ScopeSkipped 判定 ctx 是否显式跳过租户谓词。
var ScopeSkipped = gtenant.ScopeSkipped

// WithDeleted 标记 ctx 查询包含软删除行（取证/清理逃生门）。
var WithDeleted = gtenant.WithDeleted

// DeletedIncluded 判定 ctx 是否显式要求包含软删除行。
var DeletedIncluded = gtenant.DeletedIncluded

// Key 以租户为前缀拼复合键（"<id>/<rest>"）。
var Key = gtenant.Key

// Scoped 返回 GORM scope：无条件给当前语句拼 tenant_id 谓词（逐查询显式
// 形态，给不走 callback 面的裸 Table()/Raw 组装面用）。强制注入面见
// tenant.Register（storage.Open 接线）。
func Scoped(ctx context.Context) func(*gorm.DB) *gorm.DB {
	if ScopeSkipped(ctx) {
		return func(db *gorm.DB) *gorm.DB { return db }
	}
	id := FromContext(ctx)
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("tenant_id = ?", id)
	}
}
