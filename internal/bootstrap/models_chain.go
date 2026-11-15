package bootstrap

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/oagentkit/llm"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// buildGenerateChain 主生成链：无降级配置 → 单模型 Client；配置 OPENAI_FALLBACK_*
// → Resilient 主备链 + 降级 BaseChatModel（ReAct 旁路 Failover 用）。返回的
// fallbackProvider 供阶段覆盖构建各自降级链复用（nil = 无降级）。
func (m *Models) buildGenerateChain(ctx context.Context, cfg *config.Config, log logx.Logger,
	chatModel model.BaseChatModel) (llm.Provider, error) {
	fallbackReady := cfg.LLM.FallbackBaseURL != "" && cfg.LLM.FallbackAPIKey != "" && cfg.LLM.FallbackModel != ""
	if !fallbackReady {
		c := llm.NewClient(chatModel, cfg.LLM.Model, nil)
		c.ContextTokens = cfg.LLM.ContextToken
		c.MaxOutputTokens = cfg.MaxTokensResolved()
		// v5.5.2：无降级分支同样落 RETRIES/BASE_DELAY——此前该 env 只在 Resilient
		// 链生效，单模型 Client 恒用库默认（3 试/2s 基），429 退避够不着分钟级限流
		// 窗口（demoos 网关 per-model RPM 实测 10）。Client.MaxRetries 含首次，
		// env 语义「不含首次」，+1 对齐。
		c.MaxRetries = cfg.LLM.LLMResilienceRetries + 1
		c.BaseDelay = cfg.LLM.LLMResilienceBaseDelay
		c.Limiter = m.Limiter
		m.Generate = c
		return nil, nil
	}
	primary, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
		BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: cfg.LLM.Model,
		ContextTokens: cfg.LLM.ContextToken, MaxOutputTokens: cfg.MaxTokensParam(),
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		// v0.10.16：429 响应捕获 Retry-After，重试环按服务端建议退避（限流窗口
		// 还剩多久只有端点知道；mimo 并发限流下比本地指数曲线更准）
		CaptureRetryAfter: true,
	})
	if e != nil {
		return nil, fmt.Errorf("构造主 LLM Provider 失败: %w", e)
	}
	fb, e := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
		BaseURL: cfg.LLM.FallbackBaseURL, APIKey: cfg.LLM.FallbackAPIKey, Model: cfg.LLM.FallbackModel,
		ContextTokens:     cfg.LLM.FallbackContextToken,
		CaptureRetryAfter: true,
		HTTPClient:        tracedLLMClient(cfg.LLM.Timeout),
	})
	if e != nil {
		return nil, fmt.Errorf("构造降级 LLM Provider 失败: %w", e)
	}
	resilient, e := llm.NewResilient(llm.NewFallbackChain(primary, fb), llm.ResilientConfig{
		RetriesPerModel: cfg.LLM.LLMResilienceRetries,
		BaseDelay:       cfg.LLM.LLMResilienceBaseDelay,
	})
	if e != nil {
		return nil, fmt.Errorf("构造 Resilient 失败: %w", e)
	}
	// 降级事件接结构化日志（运维观测主/备切换频率）
	resilient.OnFallback = func(from, to, stage, reason string) {
		log.Warn("argus.llm.fallback", "from", from, "to", to,
			"stage", stage, "reason", reason)
		obs.IncrLLMFailover(stage)
	}
	resilient.Limiter = m.Limiter
	m.Generate = resilient

	// ReAct 主路径（ADK ChatModelAgent 直调 BaseChatModel）绕过 Resilient——
	// FailoverModel 装饰补齐主备切换（oagentkit；阶段覆盖裸模型无降级，靠它补；
	// 无覆盖时 RawModel 自带 failover，再包为无害冗余，见上方不变量注）。
	fallbackBM, e := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		BaseURL:    cfg.LLM.FallbackBaseURL,
		APIKey:     cfg.LLM.FallbackAPIKey,
		Model:      cfg.LLM.FallbackModel,
		Timeout:    cfg.LLM.Timeout,
		HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
	})
	if e != nil {
		return nil, fmt.Errorf("构造降级 BaseChatModel 失败: %w", e)
	}
	m.Fallback = fallbackBM
	m.FallbackName = cfg.LLM.FallbackModel

	log.Info("argus.boot.llm_resilient", "chain", resilient.String(),
		"primary_ctx", cfg.LLM.ContextToken, "fallback_ctx", cfg.LLM.FallbackContextToken)
	log.Info("argus.boot.llm_failover", "react", "enabled", "fallback_model", cfg.LLM.FallbackModel)
	return fb, nil
}

// buildStageLLMs P6 阶段覆盖：R1/R2 走生成链（StageRouter 注册），R3/R35/Router/QA
// 直调模型，Reviewer 覆盖链。各覆盖继承主 BaseURL/APIKey。
func (m *Models) buildStageLLMs(ctx context.Context, cfg *config.Config, log logx.Logger,
	fallbackProvider llm.Provider) error {
	buildStageModel := func(so *config.StageOverride) (model.BaseChatModel, error) {
		// v3.33.0 ctxfit：直调阶段窗口拟合生效（此前 CONTEXT_TOKENS 对直调路径
		// 静默无效——v3.8.0 的「配置误导」告警随之解除）
		if so.ContextTokens > 0 && so.ContextTokens != cfg.LLM.ContextToken {
			log.Info("argus.boot.stage_window", "model", so.Model,
				"configured_ctx", so.ContextTokens, "main_ctx", cfg.LLM.ContextToken,
				"hint", "直调阶段窗口拟合已生效（ctxfit 两级拟合，超限才动作）")
		}
		return openai.NewChatModel(ctx, &openai.ChatModelConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			MaxTokens: ptrNonNeg(cfg.MaxTokensParam()), Timeout: cfg.LLM.Timeout,
			HTTPClient: tracedLLMClient(cfg.LLM.Timeout),
		})
	}
	buildStageGen := func(so *config.StageOverride) (llm.Generator, error) {
		p, err := llm.NewOpenAIProvider(ctx, llm.OpenAIProviderConfig{
			BaseURL: cfg.LLM.BaseURL, APIKey: cfg.LLM.APIKey, Model: so.Model,
			ContextTokens: so.ContextTokens, MaxOutputTokens: cfg.MaxTokensParam(),
			Timeout:           cfg.LLM.Timeout,
			CaptureRetryAfter: true,
			HTTPClient:        tracedLLMClient(cfg.LLM.Timeout),
		})
		if err != nil {
			return nil, err
		}
		return stageGenerator(p, fallbackProvider, so, cfg, m.Limiter)
	}
	for stage, so := range map[string]*config.StageOverride{
		plugin.LayerR1: cfg.StageModelR1, plugin.LayerR2: cfg.StageModelR2,
	} {
		if so == nil {
			continue
		}
		g, err := buildStageGen(so)
		if err != nil {
			return fmt.Errorf("构造 %s 阶段模型失败: %w", stage, err)
		}
		if m.StageLLMs == nil {
			m.StageLLMs = map[string]llm.Generator{}
		}
		m.StageLLMs[stage] = g
		log.Info("argus.boot.stage_model", "stage", stage, "model", so.Model)
	}
	if so := cfg.StageModelR3; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 R3 模型失败: %w", err)
		}
		m.R3 = mm
		log.Info("argus.boot.stage_model", "stage", "R3(ReAct)", "model", so.Model)
	}
	if so := cfg.StageModelR35; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 R3.5 模型失败: %w", err)
		}
		m.Reflect = mm
		log.Info("argus.boot.stage_model", "stage", "R35(reflection)", "model", so.Model)
	}
	if so := cfg.StageModelRouter; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 Router 模型失败: %w", err)
		}
		m.Router = mm
		log.Info("argus.boot.stage_model", "stage", "router", "model", so.Model)
	}
	if so := cfg.StageModelReviewer; so != nil {
		g, err := buildStageGen(so)
		if err != nil {
			return fmt.Errorf("构造 Reviewer 模型失败: %w", err)
		}
		m.ReviewerGen = g
		m.ReviewerName = so.Model
		log.Info("argus.boot.stage_model", "stage", "reviewer", "model", so.Model)
	}
	if so := cfg.StageModelQA; so != nil {
		mm, err := buildStageModel(so)
		if err != nil {
			return fmt.Errorf("构造 QA 模型失败: %w", err)
		}
		m.QA = mm
		log.Info("argus.boot.stage_model", "stage", "qa", "model", so.Model)
	}
	return nil
}
