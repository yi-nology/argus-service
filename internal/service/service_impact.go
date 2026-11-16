// service_impact.go —— 影响面视图数据面（v3.183.0）：MCP get_impact 与
// GET /tasks/:id/impact（交互 HTML）共用。快照由 runner 影响面管线即时落
// artifacts_json（任务 running 期即可消费，评审失败也不丢）。

package service

import (
	"context"
	"net/url"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/yi-nology/argus/internal/impactview"
	"github.com/yi-nology/argus/internal/storage"
)

// prevImpactCompare 同 MR 上一轮 done 任务的影响面快照集合差。
// 无上一轮/无快照/解码失败一律 nil（无对比面,不阻断主图）。
func (s *Service) prevImpactCompare(ctx context.Context, cur *storage.Task) *impactview.ImpactCompare {
	return prevImpactCompare(s, ctx, cur)
}

// ImpactShareVerifier 影响面 share 链接校验器（v5.6.1，main 装配进
// routerapi.SetImpactShare）：?share= 命中任务绑定 HMAC 令牌即放行该任务的
// /impact 页，否则回落 Bearer（middleware 层组合）。secret 取 API master
// token，为空（鉴权关闭）恒 false——此场景下 Bearer 本就不拦。
func (s *Service) ImpactShareVerifier() func(*app.RequestContext) bool { return impactShareVerifier(s) }

// impactSnapshot 任务影响面快照。无任务/域外任务/无快照/坏 JSON 一律 false
// （域外按不存在处理，不泄露存在性——TaskStages 同纪律）。
func (s *Service) impactSnapshot(ctx context.Context, taskID string) (*impactview.Snapshot, *storage.Task, bool) {
	return impactSnapshot(s, ctx, taskID)
}

// ImpactView MCP get_impact 数据面：快照 map 形态（附任务元信息）。
func (s *Service) ImpactView(ctx context.Context, taskID string) (map[string]any, bool) {
	return impactView(s, ctx, taskID)
}

// ImpactViewHTML 交互视图：自包含单文件 HTML（零外链，内网可用；可离线存档）。
func (s *Service) ImpactViewHTML(ctx context.Context, taskID string) (string, bool) {
	return impactViewHTML(s, ctx, taskID)
}

// repoMapLink 整仓地图互跳链接（share 令牌可选注入）。
func repoMapLink(secret, platform, owner, repo string) string {
	link := "/repos/map?platform=" + url.QueryEscape(platform) +
		"&owner=" + url.QueryEscape(owner) + "&repo=" + url.QueryEscape(repo)
	if tok := impactview.ImpactShareToken(secret, repoMapShareID(platform, owner, repo)); tok != "" {
		link += "&share=" + tok
	}
	return link
}
