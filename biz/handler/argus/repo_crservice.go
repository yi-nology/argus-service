// repo_crservice.go RepoCRs handler（v8.8.1：仓库 PR/MR 列表——控制台
// 「PR 号下拉」数据面，平台 ChangeRequest 面）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	argus "github.com/yi-nology/argus/biz/model/argus"
)

// RepoCRs .
// @router /repos/crs [GET]
func RepoCRs(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	var req argus.RepoCRsRequest
	if !bind(ctx, c, &req) {
		return
	}
	state, limit := "", 0
	if req.State != nil {
		state = *req.State
	}
	if req.Limit != nil {
		limit = int(*req.Limit)
	}
	data, status := Svc.RepoCRs(ctx, req.Platform, req.Owner, req.Repo, state, limit)
	reply(c, status, data, "查询 PR 列表失败")
}
