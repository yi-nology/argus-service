// config_llm.go —— LLM 端点与降级链配置。

package config

import "time"

// LLMConfig OpenAI 兼容端点（复用 lacus/.env）。
type LLMConfig struct {
	BaseURL      string
	APIKey       string
	Model        string
	Timeout      time.Duration
	MaxTokens    int // 单次生成上限；0 = 按 MaxTokensResolved 从窗口派生；推理模型（如 mimo）需 >= 4096
	ContextToken int // 模型上下文窗口（token 数，输入+输出合计），其余上限的唯一派生基准

	// 降级备选模型（可选）：四项全配时启用 Resilient 多模型降级链，
	// 主模型失败（429/超时/超窗等）自动切换到备选。
	FallbackBaseURL      string
	FallbackAPIKey       string
	FallbackModel        string
	FallbackContextToken int

	// 降级链调优（可选）：
	LLMResilienceRetries   int           // 每模型重试次数（默认 2）
	LLMResilienceBaseDelay time.Duration // 重试退避基准（默认 2s）
}
