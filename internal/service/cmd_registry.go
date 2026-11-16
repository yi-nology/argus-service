// cmd_registry.go —— @bot 评论指令目录与信息类指令（v5.3.0 商业化打磨）。
//
// 指令目录单源：help 渲染、门控判定（isDriveCommand）、冷却提示（matchAnyCommand）
// 共用一张表——新增指令只改 cmdCatalog + 对应 extract*/dispatch*，help 不会漏项。
//
// 商业化三件：
//   - help/帮助：任何时刻可查指令手册（免节流，查询类永远畅通）；
//   - status/进度：最近一轮审查状态与结论（免节流）；
//   - 烧预算指令门控（ARGUS_COMMENT_GATE）：review/recheck/fix/testgen 仅协作者
//     可触发，防公开仓路人评论白烧审查预算；问答/查询/误报反馈类不受门控。

package service

import (
	"context"
	"strings"

	datacache "github.com/oekit/data/cache"
)

// dispatchInfo 信息类指令（help/status）：免节流、免门控、零 LLM。
// 返回 true 表示已消费该评论。
func (s *Service) dispatchInfo(ctx context.Context, question, platform, owner, repo,
	prNum, prRef, delivery string) bool {
	return dispatchInfo(s, ctx, question, platform, owner, repo, prNum, prRef, delivery)
}

// statusText 审查状态（最近一轮任务 + 意见数 + 历史轮数）。
func (s *Service) statusText(ctx context.Context, platform, owner, repo, prNum string) string {
	return statusText(s, ctx, platform, owner, repo, prNum)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// collabCache 协作者名单缓存（per-Service 懒建，TTL 5 分钟）。
func (s *Service) collabCache() *datacache.LocalCache[string] { return collabCache(s) }

// ackComment 即时回执（v5.3.0）：给触发指令的评论加 👀，告诉用户"已看到"。
// best-effort：能力缺失/失败只留痕，绝不影响主链路。
func (s *Service) ackComment(ctx context.Context, platform, owner, repo string, commentID int64) {
	ackComment(s, ctx, platform, owner, repo, commentID)
}

// actorCanDrive 烧预算指令触发者门控（v5.3.0）。
// ARGUS_COMMENT_GATE=open 直接放行；collaborator 模式查协作者名单（每仓 5 分钟
// 缓存，名单以 "\nu1\nu2\n" 形态缓存、本地子串判定）。平台不支持
// CollaboratorManager 或查询失败时 fail-open 并留痕——能力差异/网络抖动不能把
// 评论链路整个堵死。
func (s *Service) actorCanDrive(ctx context.Context, platform, owner, repo, actor string) bool {
	return actorCanDrive(s, ctx, platform, owner, repo, actor)
}
