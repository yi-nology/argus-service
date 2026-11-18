// service_audit_tenant.go —— 审计租户字段注入（#103）。
//
// oagentkit/obs/audit 的 Log(action, fields...) 无 ctx 形参——租户归属从
// 调用点 ctx 提取后注入 kv 对，统一走本 helper（18 处调用点同口径，防漏注）。
// nil Audit/空 ctx 安全（tenant.FromContext 归一 default）。

package service

import (
	"context"

	"github.com/oagentkit/obs/audit"

	"github.com/yi-nology/argus/internal/tenant"
)

// audit 租户感知审计：ctx 租户 + 操作者 token 指纹自动并入字段（已有同名字段
// 后者不覆盖——调用方显式值优先）。
func (s *Service) audit(ctx context.Context, action audit.Action, fields ...any) {
	fields = append(fields, "tenant", tenant.FromContext(ctx))
	if fp := TokenFPFromContext(ctx); fp != "" {
		fields = append(fields, "operator_fp", fp)
	}
	s.Audit.Log(action, fields...)
}
