package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// RevokeShare POST /controlplane/shares/revoke（#91）：撤销 share 令牌。
func RevokeShare(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	var req argus.ControlPlaneRevokeShareRequest
	if !bind(ctx, c, &req) {
		return
	}
	resp, status := Svc.RevokeShare(ctx, &req)
	reply(c, status, resp, "撤销 share 令牌失败")
}
