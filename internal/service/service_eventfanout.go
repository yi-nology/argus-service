// service_eventfanout.go —— 事件 Outbox fanout 环（v6.9.0 P1-05，#10）：
// 每实例一个轮询协程，把 outbox 新行回灌本地 Bus——连接本实例的 SSE 客户端
// 即可收到他实例发布的事件（事件源实例已本地广播，回灌同 ID 事件去重跳过）。
// SSE handler 语义不变（Bus 订阅 + AuthorizeEvent 按仓过滤）。

package service

import (
	"context"
	"time"
)

// outboxFanoutInterval 轮询间隔：跨实例事件延迟上界 ≈ interval + Bus 广播即时。
// 500ms 已低于 SSE keepalive（15s）粒度；LISTEN/NOTIFY 是延迟优化预留（不改语义）。
const outboxFanoutInterval = 500 * time.Millisecond

// outboxSeenCap 去重环容量：回灌协程读到的「本实例刚发过」事件 ID 集合上界。
// 双写窗口内的重复形态只有"本实例 500ms 内发过"——容量按事件峰值 100/s 量级
// 放 4096 足够；FIFO 淘汰。
const outboxSeenCap = 4096

// outboxRetention outbox 行保留期（观测面数据）：fanout 消化后仅审计价值。
const outboxRetention = 24 * time.Hour

// StartOutboxFanout 启动事件回灌环（bootstrap 装配；ctx 取消即退出，停机排水
// 无需显式等待——轮询协程无在途外部副作用）。nil Bus/Store 安全（测试栈）。
func (s *Service) StartOutboxFanout(ctx context.Context) { startOutboxFanout(s, ctx) }
