// service_console_members.go —— GET /auth/members 实例成员清单（v8.8.0 控制台
// 成员视图）：admin 管理面投影，凭证字段（PasswordHash/Salt）零外泄。
package service

import (
	"context"
	"net/http"
)

// Members 成员清单（用户名/角色/创建时间；admin 角色在方法内判定）。
func (s *Service) Members(ctx context.Context) (map[string]any, int) {
	if !canManage(ctx) {
		authzDeniedMetric(ctx, "/auth/members")
		return map[string]any{"error": "成员清单仅 admin 角色可查看"}, http.StatusForbidden
	}
	users, err := s.Store.Auths().ListUsers(ctx)
	if err != nil {
		s.log().Error("argus.auth.members_list_failed", "error", err.Error())
		return map[string]any{"error": "查询失败"}, http.StatusInternalServerError
	}
	items := make([]map[string]any, 0, len(users))
	for _, u := range users {
		items = append(items, map[string]any{
			"username":   u.Username,
			"role":       u.Role,
			"created_at": u.CreatedAt.Unix(),
		})
	}
	return map[string]any{"members": items}, http.StatusOK
}
