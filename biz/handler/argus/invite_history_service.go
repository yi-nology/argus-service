// invite_history_service.go InviteHistory handler（v8.8.2：邀请历史，admin 面）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// InviteHistory .
// @router /auth/invites/history [GET]
func InviteHistory(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.Invites(ctx)
	reply(c, status, data, "查询邀请历史失败")
}
