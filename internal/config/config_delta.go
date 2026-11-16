// config_delta.go —— agent.delta 的分发/授权开关（#67）：从 Config 主块拆出
// （config_types.go 文件长度阈值），内嵌提升字段——cfg.DeltaFanout /
// cfg.DeltaReasoning 直读。
package config

// DeltaStream delta 分发面 + reasoning 授权面（Config 内嵌）。
type DeltaStream struct {
	// DeltaFanout delta 跨实例分发开关（ARGUS_DELTA_FANOUT，默认 true）：
	// true = 与可靠状态事件同管道（本地 Bus + Outbox 跨实例 fanout 回灌，
	// 双副本下任一副本的 SSE 客户端都能看到流式增量）；false = 回滚
	// live-only（只发本地 Bus，不落 outbox），行为与 v7.4.x 逐字一致。
	DeltaFanout bool
	// DeltaReasoning reasoning 相位的对外授权面（ARGUS_DELTA_REASONING，缺省
	// admin）：admin = 仅 admin 档 token 可订阅内部推理增量（read/ci 连接在
	// SSE 出口被过滤，普通只读 token 收不到）；all = 全部已鉴权 scope（排障
	// 显式开启）；off = 任何 token 都不下发 reasoning（最严脱敏）。未知值回落
	// admin（fail-closed）。该面只管 SSE 出口过滤，不影响事件的产生与 outbox
	// 存储（服务端内部事实源完整）。
	DeltaReasoning string
}
