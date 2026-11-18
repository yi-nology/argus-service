// service_tenant_profile.go —— 租户 profile 消费点（#101 等）。

package service

import (
	"context"

	"github.com/yi-nology/argus/internal/tenant"
)

// botUsernameOf 租户感知回帖身份（#101）：租户 profile 的 BotUsername 覆盖
// 优先，回落全局默认。自回环抑制/评论提问提取/report 头作者校验全走本
// 单源——两租户配不同 bot 账号时不再互相触发回环或漏抑制。
func (s *Service) botUsernameOf(ctx context.Context) string {
	if p := s.Cfg.ProfileFor(tenant.FromContext(ctx)); p.BotUsername != "" {
		return p.BotUsername
	}
	return s.Cfg.BotUsername
}
