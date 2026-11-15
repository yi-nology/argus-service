package argusctx

import (
	"net/http"

	"github.com/yi-nology/go-git-platform/provider"
)

// extractDeliveryID 实现 SDKCtx.ExtractDeliveryID（平台 delivery ID 提取）。
func extractDeliveryID(s *SDKCtx, headerGetter func(key string) string) string {
	for _, h := range DeliveryHeaders {
		if v := headerGetter(h); v != "" {
			return v
		}
	}
	return ""
}

// parseWebhook 实现 SDKCtx.ParseWebhook（签名校验 + 事件解析）。
func parseWebhook(s *SDKCtx, platform string, r *http.Request, secret string) (*provider.NormalizedEvent, error) {
	p, err := s.Provider(platform)
	if err != nil {
		return nil, err
	}
	return p.ParseWebhookEvent(r, secret)
}
