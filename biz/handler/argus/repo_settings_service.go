// repo_settings_service.go —— 服务侧按仓配置路由 handler（v3.187.0）。
// 路由自 v3.189.0 起由 idl/argus.thrift 的 RepoSettingsService 生成承载；
// 鉴权 authMw（routerapi.SetAuth），写档（PUT/DELETE）由 service.requiredScope
// 收口为 admin、读档 read。
package argus

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mw "github.com/oekit/middleware"
)

// repoSettingsBodyMax 写入体上限：三个整数旋钮的规范 JSON 远小于此，上限只为
// 防滥用（错误内容 Parse 会拒）。
const repoSettingsBodyMax = 4_096

// repoWildcardParam 取仓库路径参数：路由是 /*repo 通配（GitLab 子组仓库名
// 含 "/"，:repo 单段路由在 %2F 解码后匹配失败——43 实测 404）。通配值可能带
// 前导 "/"（视 hertz 版本），归一为空串或裸仓库名。
func repoWildcardParam(c *app.RequestContext) string {
	return strings.TrimPrefix(c.Param("repo"), "/")
}

// RepoSettingsGet 单仓配置视图（覆盖集 + 全局生效值）。
// @router /repo-settings/:platform/:owner/*repo [GET]
func RepoSettingsGet(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.RepoSettingsView(ctx,
		c.Param("platform"), c.Param("owner"), repoWildcardParam(c))
	replyMsg(c, status, data, "ok", viewErr(data))
}

// RepoSettingsList 全部按仓配置（观测面）。
// @router /repo-settings [GET]
func RepoSettingsList(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.ListRepoSettings(ctx)
	reply(c, status, data, "按仓配置查询失败")
}

// RepoSettingsPut 写入单仓覆盖（admin 档；body 为 reposettings.Settings JSON）。
// @router /repo-settings/:platform/:owner/*repo [PUT]
func RepoSettingsPut(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	body := c.Request.Body()
	if len(body) == 0 {
		mw.ErrorResp(c, consts.StatusBadRequest, "请求体为空")
		return
	}
	if len(body) > repoSettingsBodyMax {
		mw.ErrorResp(c, consts.StatusBadRequest, "请求体超过 4KB 上限")
		return
	}
	data, status := Svc.UpsertRepoSettings(ctx,
		c.Param("platform"), c.Param("owner"), repoWildcardParam(c), body)
	replyMsg(c, status, data, "repo settings updated", viewErr(data))
}

// RepoSettingsDelete 移除单仓覆盖（admin 档；幂等）。
// @router /repo-settings/:platform/:owner/*repo [DELETE]
func RepoSettingsDelete(ctx context.Context, c *app.RequestContext) {
	if !ensureSvc(c) {
		return
	}
	data, status := Svc.DeleteRepoSettings(ctx,
		c.Param("platform"), c.Param("owner"), repoWildcardParam(c))
	replyMsg(c, status, data, "ok", viewErr(data))
}

// viewErr service 返回 map 里携带的错误文案（校验失败时 data 非nil带 error 键）。
func viewErr(data map[string]any) string {
	if msg, ok := data["error"].(string); ok && msg != "" {
		return msg
	}
	return "请求失败"
}
