// repo_info_service.go RepoBranches handler（v8.8.0：仓库分支清单——控制台
// 分支下拉数据面，平台 SDK BranchManager + 默认分支）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// RepoBranches .
// @router /repos/branches [GET]
func RepoBranches(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	var req argus.RepoBranchesRequest
	if !bind(ctx, c, &req) {
		return
	}
	data, status := Svc.RepoBranches(ctx, req.Platform, req.Owner, req.Repo)
	reply(c, status, data, "查询分支失败")
}
