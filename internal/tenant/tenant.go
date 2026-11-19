// tenant.go —— 租户标识、ctx 贯穿与 GORM 强制过滤 scope。

package tenant

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// Default 缺省租户：未声明租户的一切主体（存量数据行、无 tenant 声明的 token、
// 内部执行路径）都归入本租户。ID 显式字符串而非空串——空串在 SQL 谓词与
// 缓存键拼接里语义含混（NULL/”/missing 三态），显式 "default" 单态可判。
const Default = "default"

// ctxKey 租户 ctx 键（私有类型防跨包键碰撞）。
type ctxKey struct{}

// Normalize 租户 ID 归一：空/纯空白 → Default；其余去空白原样返回。
// 唯一归一出口——存储谓词、缓存键前缀、审计字段全走它，杜绝三态漂移。
func Normalize(id string) string {
	if t := strings.TrimSpace(id); t != "" {
		return t
	}
	return Default
}

// NewContext 把租户写入 ctx（写入前归一）。
func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, Normalize(id))
}

// FromContext 从 ctx 取租户 ID；未写入（内部路径/旧调用）→ Default。
// 存储层每条查询的 tenant 谓词都以此为准——fail-closed 方向是「无声明 =
// 默认租户」而非「无声明 = 全可见」：默认租户只是历史数据的归属桶，
// 可见性仍由 authz 层（token pattern/租户声明）决定，两者正交。
func FromContext(ctx context.Context) string {
	if ctx == nil {
		return Default
	}
	if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
		return v
	}
	return Default
}

// Scoped 返回 GORM scope：无条件给当前语句拼 tenant_id 谓词（#94 机制面——
// 过滤从「每个端点自觉加 where」变成「存储层强制注入」）。表无 tenant_id 列
// （全局基础设施表 leader_leases/schema_migrations）的查询不得走本 scope。
//
// 注意：Raw/Exec 裸 SQL 不经过 scope，裸 SQL 站点的租户条件须手工拼（治理
// 口径：新增裸 SQL 必须带 tenant 谓词）。
func Scoped(ctx context.Context) func(*gorm.DB) *gorm.DB {
	if ScopeSkipped(ctx) {
		return func(db *gorm.DB) *gorm.DB { return db }
	}
	id := FromContext(ctx)
	return func(db *gorm.DB) *gorm.DB {
		return db.Where("tenant_id = ?", id)
	}
}

// Key 以租户为前缀拼复合键（缓存键/文件路径段/副作用资源串共用形态）：
// 返回 "<id>/<rest>" 统一形态，同坐标资源跨租户天然分桶。rest 为空返回 "<id>"。
func Key(id, rest string) string {
	id = Normalize(id)
	if rest == "" {
		return id
	}
	return id + "/" + rest
}

// skipKey 强制跳过租户谓词注入的 ctx 键：控制面跨租户运维操作（全库对账/
// 列举）的显式逃生门。机制而非纪律——普通查询永远不带这个键。
type skipKey struct{}

// WithoutScope 标记 ctx 为「跳过租户谓词注入」（逃生门；调用方须在注释里
// 写明跨租户理由并接受审计面）。storage 回调与 tenant.Scoped 都认这个标记。
func WithoutScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipKey{}, true)
}

// ScopeSkipped 判定 ctx 是否显式跳过租户谓词。
func ScopeSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(skipKey{}).(bool)
	return v
}

// includeDeletedKey 软删除可见性 ctx 键：默认所有查询不可见 deleted_at 非空行
//（#109a 软删除机制面，同 Scoped 的强制注入形态）；清理/恢复/取证类操作显式
// 带此标记才看得见。机制而非纪律——普通查询永远不带这个键。
type includeDeletedKey struct{}

// WithDeleted 标记 ctx「查询包含软删除行」（逃生门；调用方须在注释写明理由）。
// storage 软删除谓词回调与消费方共享此标记。
func WithDeleted(ctx context.Context) context.Context {
	return context.WithValue(ctx, includeDeletedKey{}, true)
}

// DeletedIncluded 判定 ctx 是否显式要求包含软删除行。
func DeletedIncluded(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(includeDeletedKey{}).(bool)
	return v
}
