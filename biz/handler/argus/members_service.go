// members_service.go Members handler（v8.8.0：实例成员清单，admin 面）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// Members .
// @router /auth/members [GET]
func Members(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.Members(ctx)
	reply(c, status, data, "查询成员失败")
}
