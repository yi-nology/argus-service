// service_console_invites.go —— GET /auth/invites/history 邀请历史（v8.8.2）：
// admin 管理面，token 零投影，状态服务端派生 pending|used|expired。
package service

import (
	"context"
	"net/http"
	"time"
)

// Invites 邀请历史（admin 角色在方法内判定）。
func (s *Service) Invites(ctx context.Context) (map[string]any, int) {
	if !canManage(ctx) {
		authzDeniedMetric(ctx, "/auth/invites/history")
		return map[string]any{"error": "邀请清单仅 admin 角色可查看"}, http.StatusForbidden
	}
	rows, err := s.Store.Auths().ListInvites(ctx)
	if err != nil {
		s.log().Error("argus.auth.invites_list_failed", "error", err.Error())
		return map[string]any{"error": "查询失败"}, http.StatusInternalServerError
	}
	now := time.Now()
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		inv := &rows[i]
		status := "pending"
		if inv.UsedAt != nil {
			status = "used"
		} else if !inv.ExpiresAt.IsZero() && inv.ExpiresAt.Before(now) {
			status = "expired"
		}
		item := map[string]any{
			"invite_id":  inv.ID,
			"role":       inv.Role,
			"status":     status,
			"username":   inv.Username,
			"created_by": inv.CreatedBy,
			"created_at": inv.CreatedAt.Unix(),
			"expires_at": inv.ExpiresAt.Unix(),
		}
		if inv.UsedAt != nil {
			item["used_at"] = inv.UsedAt.Unix()
		}
		items = append(items, item)
	}
	return map[string]any{"invites": items}, http.StatusOK
}
