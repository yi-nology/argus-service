// service_delta_egress.go —— agent.delta 的跨实例分发与 reasoning 相位出口授权
// （#67）。
//
// 两个边界各自收口：
//   - 分发面：ARGUS_DELTA_FANOUT（发布路径在 runner/service 的 publishDelta/
//     publishQADelta 单点判定）——delta 与可靠状态事件同管道（Bus+Outbox），
//     =0 恢复 live-only（v7.4.x 行为）。
//   - 授权面：AuthorizeDeltaPhase —— SSE 出口的相位过滤（/events handler 逐
//     事件调用，与 AuthorizeEvent 同点）。reasoning 相位默认只对 admin 档
//     token 下发：内部推理不是普通只读 token 可订阅的负载（read/ci 连接直接
//     过滤丢弃，不脱敏转发）；ARGUS_DELTA_REASONING=all/off 提供显式的放宽/
//     收紧面。事件照常产生与落 outbox（服务端事实源完整），过滤只在出口。
package service

import (
	"context"
	"strings"
)

// deltaReasoningAdmin / deltaReasoningAll / deltaReasoningOff ARGUS_DELTA_REASONING
// 词表（normalizeDeltaReasoning 归一，未知值回落 admin）。
const (
	deltaReasoningAdmin = "admin"
	deltaReasoningAll   = "all"
	deltaReasoningOff   = "off"
)

// AuthorizeDeltaPhase SSE agent.delta 相位出口判定（#67）：reasoning 相位按
// ARGUS_DELTA_REASONING 面（admin|all|off）过滤，其余相位（text/reset）不过
// 此门——「可展示文本增量」本就是观测面的一部分。拒绝计数进 authzDenied
// 指标（route=events.delta_reasoning），泄露尝试可观测。
func (s *Service) AuthorizeDeltaPhase(ctx context.Context, phase string) bool {
	if !strings.EqualFold(phase, "reasoning") {
		return true
	}
	mode := deltaReasoningAdmin
	if s != nil && s.Cfg != nil && s.Cfg.DeltaReasoning != "" {
		mode = strings.ToLower(strings.TrimSpace(s.Cfg.DeltaReasoning))
	}
	switch mode {
	case deltaReasoningAll:
		return true
	case deltaReasoningOff:
		authzDeniedMetric(ctx, "events.delta_reasoning")
		return false
	default: // admin：仅 admin 档可订阅内部推理
		if ScopeFromContext(ctx) == "admin" {
			return true
		}
		authzDeniedMetric(ctx, "events.delta_reasoning")
		return false
	}
}
