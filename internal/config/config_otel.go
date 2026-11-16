// config_otel.go —— OTel 导出配置组（自 config_types.go 拆出，职责独立：
// 可观测出口的开关/协议/认证；变化原因是「接哪个后端」而不是业务配置）。

package config

// otelConfig OTLP 链路导出配置（v3.163.0，oekit/tracing 接入，ARGUS_OTEL_ENDPOINT
// 非空启用）：全局 TracerProvider + W3C/B3 传播器 + gormotel 挂主库——DB 查询
// 成 span 导出；LLM 调用经 httpotel transport + langfuseobs GenAI span。
// 缺省空 = 完全不 Init、不产生任何网络连接（零开销）。
type otelConfig struct {
	OTelEndpoint string
	// OTelProtocol 导出协议（ARGUS_OTEL_PROTOCOL：grpc|http；缺省 http——
	// jaeger all-in-one/Langfuse 类后端走 HTTP 接收端）。
	OTelProtocol string
	// OTelInsecure 跳过 TLS（ARGUS_OTEL_INSECURE，内网缺省 true）。
	OTelInsecure bool
	// OTelSampleRate 采样率（ARGUS_OTEL_SAMPLE_RATE，0=全量）。
	OTelSampleRate float64
	// OTelHeaders 随导出请求带的附加头（ARGUS_OTEL_HEADERS，`K1:V1;K2:V2`；
	// ParseOTelHeaders 解析）。Langfuse OTLP 端点用它传 Basic auth
	// （`Authorization: Basic <base64(pk:sk)>`，部署侧铸好整串，仓内不拼装）。
	OTelHeaders map[string]string
}
