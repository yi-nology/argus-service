// service_caps.go —— 能力面快照（v3.129.0，MCP get_capabilities 数据面）：
// 一次看清「这个部署开了什么」——外部 agent 免盲调报错探测（fix 未配/
// 检索未挂等在这里先知道，而不是调用工具吃错误文案）。

package service

import (
	"context"
)

// Capabilities 能力面快照（纯读配置与装配态，零 IO）。
func (s *Service) Capabilities(context.Context) map[string]any { return capabilities(s) }
