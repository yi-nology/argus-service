// console_options_service.go ConsoleOptions handler（v8.7.0：控制台下拉选项
// 聚合面——platforms/kinds/statuses/repos 单源词表，前端坐标选择器数据面）。
package argus

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// ConsoleOptions .
// @router /console/options [GET]
func ConsoleOptions(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.ConsoleOptions(ctx)
	reply(c, status, data, "选项查询失败")
}
