package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/oekit/concurrency/ratelimit"

	"github.com/oekit/logx"
	"github.com/oekit/pkg/httpjson"
	"github.com/oekit/tracing/httpotel"

	"github.com/oagentkit/llm"

	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/egress"
)

// tracedLLMClient LLM 出站 HTTP 客户端（v3.177.0，llm v0.15 HTTPClient +
// eino-ext HTTPClient 注入缝）：Transport 挂 oekit/tracing httpotel——LLM 调用
// 成 OTel span 导出（此前 OTel 只有 gormotel DB 查询面，LLM 主耗时面无 trace）。
// 注入的 Client 完全接管超时（两侧同一语义：设了 HTTPClient 就不再用
// config.Timeout），timeout<=0 取 provider 缺省 60s 行为同款。
//
// v3.182.0：外层再包 httpjson.RetryAfterTransport——429 的 Retry-After 头写入
// ctx sink，供 agentrun 重试环按服务端建议退避（比本地指数准）。这是**单点**捕获：
// eino ChatModel 直调出口（ReAct，无 CaptureRetryAfter 可设）与 oagentkit Provider
// 出口（httpClient 会再套一层，RetryAfterTransport 幂等——只是重复 set 同值，无害）。
// 此前只有 Provider 出口捕获、ReAct 主路径漏（agentrun 已注入 sink 但无人写入）。
func tracedLLMClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: httpjson.RetryAfterTransport{
		Base: httpotel.WrapTransport(http.DefaultTransport),
	}}
}

// Models 装配期一次解析完成的模型栈。原散在 main.go 的 ~270 行 if/else 收敛于此：
// 主模型 + 全局降级链（OPENAI_FALLBACK_*）+ P6 阶段覆盖（ARGUS_MODEL_{R1,R2,R3,R35,
// REVIEWER,QA,ROUTER}）。不变量：
//   - Generator 路径（workflow 生成 / Reviewer 覆盖链）走 Resilient（主备）或 Client；
//   - 所有 BaseChatModel 直调出口（ReAct/单轮直调）统一包 FailoverModel——
//     阶段覆盖的**裸模型**（R3Model，buildStageModel 出 openai.NewChatModel，无降级）
//     靠它补齐主备切换（此包装是其唯一 failover，勿删）；无覆盖时 r3Chat 来自
//     Resilient.RawModel（v0.10.9+ 已是 RawModelWithFailover，本身 failover 感知），
//     再包一次属无害冗余（总败时多试一次 fallback）——故统一包而非按来源分支；
//   - 阶段覆盖继承主 BaseURL/APIKey；fallback 模型自身不做二级覆盖。
//
// 字段 nil 语义：Ready()==false 时全部为 nil（仅 cli 插件可用）；其余覆盖类字段
// （R3/Reflect/QA/Router/ReviewerGen/StageLLMs）nil = 未覆盖，消费方按各自回退链
// 取缺省（见 runner.buildWorkflow / service.routerChatModel）。
type Models struct {
	// React 主模型 BaseChatModel 出口（已包 Failover；无降级配置时即原始 chatModel）。
	// runner.Model / service.Model / registry Deps.Model 均取本字段。
	React model.BaseChatModel
	// Generate 主生成链（Resilient 多模型降级或单模型 Client）；workflow 每任务经
	// BudgetInjector 注入预算后使用。
	Generate llm.Generator
	// Limiter 全局 LLM 限速器（ARGUS_LLM_RATE_LIMIT > 0 时非 nil；runner 透传）。
	Limiter ratelimit.Limiter
	// Fallback 全局备模型 BaseChatModel（nil = 未配置降级）。runner 对 R3 旁路
	//（R3Model 为 nil 时）用 Fallback+FallbackName 再包一层 Failover。
	Fallback     model.BaseChatModel
	FallbackName string

	// StageLLMs R1/R2 阶段覆盖生成链（经 llm.StageRouter 注册进 workflow）。
	StageLLMs map[string]llm.Generator
	// R3 ReAct 专用覆盖（ARGUS_MODEL_R3；已包 Failover）。
	R3 model.BaseChatModel
	// Reflect R3.5 报告反思覆盖（ARGUS_MODEL_R35；已包 Failover）。
	Reflect model.BaseChatModel
	// ReviewerGen/ReviewerName R4 Reviewer 覆盖链（ARGUS_MODEL_REVIEWER）。
	ReviewerGen  llm.Generator
	ReviewerName string
	// ReviewerReact Reviewer 的 ReAct 出口：有覆盖时取覆盖链 RawModel 再包 Failover，
	// 否则与 React 同源。
	ReviewerReact model.BaseChatModel
	// QA Q&A 专用覆盖（ARGUS_MODEL_QA；已包 Failover）。
	QA model.BaseChatModel
	// Router 评论意图分类覆盖（ARGUS_MODEL_ROUTER；已包 Failover）。
	Router model.BaseChatModel
}

// BuildModels 解析全部模型角色。构造失败返回 error（调用方决定终止方式）；
// 未配置 OPENAI_* 不是错误——返回空栈并告警（仅 cli 插件形态）。
func BuildModels(ctx context.Context, cfg *config.Config, log logx.Logger) (*Models, error) {
	m := &Models{}
	if cfg.LLM.BaseURL == "" || cfg.LLM.APIKey == "" || cfg.LLM.Model == "" {
		log.Warn("argus.boot.no_llm", "hint", "OPENAI_* 未配置，builtin 插件不可用（仅 cli 插件可工作）")
		return m, nil
	}
	// T7 出站同意门：外部端点按 ARGUS_LLM_EGRESS_CONSENT 收紧；拒绝时不建模型
	// （Ready()==false，审查/Q&A 降级为不可用——比带病外发更安全）。
	if ok, reason := egress.CheckLLMConsent(log, cfg.LLM.BaseURL); !ok {
		log.Warn("argus.boot.llm_egress_blocked", "reason", reason,
			"hint", "显式设 ARGUS_LLM_EGRESS_CONSENT=yes 同意外发，或改用 loopback 端点")
		return m, nil
	}

	// ---- LLM 全局限速器（token bucket；ARGUS_LLM_RATE_LIMIT > 0 时启用）----
	// v3.5.9 起覆盖主链 Client/Resilient（历史缺口：此前只到达非降级路径的阶段
	// 覆盖 Client）。ReAct RawModel 旁路不经 Client/Resilient，仍不受限（oagentkit 侧限制）。
	if cfg.LLMRateLimit > 0 {
		m.Limiter = ratelimit.NewTokenBucketFloat(cfg.LLMRateLimit, cfg.LLMRateBurst)
		log.Info("argus.boot.llm_rate_limiter", "rps", cfg.LLMRateLimit, "burst", cfg.LLMRateBurst)
	}

	maxTokens := cfg.MaxTokensParam()
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:    cfg.LLM.BaseURL,
		APIKey:     cfg.LLM.APIKey,
		Model:      cfg.LLM.Model,
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		// 负值 = 不下发 max_tokens（oagentkit 语义，推理模型兼容）
		MaxTokens: ptrNonNeg(maxTokens),
	})
	if err != nil {
		return nil, fmt.Errorf("构造 LLM 客户端失败: %w", err)
	}
	m.React = chatModel

	// 全局降级链：配置 OPENAI_FALLBACK_* 四项后启用 Resilient 多模型降级。
	fallbackProvider, err := m.buildGenerateChain(ctx, cfg, log, chatModel)
	if err != nil {
		return nil, err
	}
	// T7：主 Generate 链挂出站掩码 + 哈希审计（零原文副本）。
	m.Generate = wrapEgressAudit(m.Generate, log)
	log.Info("argus.boot.llm_ready", "model", cfg.LLM.Model, "base_url", cfg.LLM.BaseURL,
		"context_tokens", cfg.LLM.ContextToken, "max_tokens", maxTokens,
		"diff_chars", cfg.MaxDiffCharsResolved(), "task_budget", cfg.EffectiveTaskTokenBudget())

	// ---- P6 分阶段模型路由 ----
	if err := m.buildStageLLMs(ctx, cfg, log, fallbackProvider); err != nil {
		return nil, err
	}
	// Failover 包装：仅配置了全局降级时生效；每个直调出口按各自 stage 名标注切换日志。
	m.wrapFailover(cfg, log)
	// ---- 上下文窗口拟合 + 输出续写（v3.33/v3.34）----
	m.wrapFit(cfg, log)
	return m, nil
}
