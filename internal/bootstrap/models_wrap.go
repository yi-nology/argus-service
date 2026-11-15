package bootstrap

import (
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/oagentkit/llm"
	"github.com/oagentkit/llm/wrap"
	"github.com/oekit/concurrency/ratelimit"
	"github.com/oekit/logx"
	"github.com/yi-nology/argus/internal/config"
	"github.com/yi-nology/argus/internal/obs"
	"github.com/yi-nology/argus/internal/plugin"
)

// wrapFailover FailoverModel 包装：仅配置了全局降级时生效，每个直调出口按各自
// stage 名标注切换日志；ReviewerReact 兜底（=主 React 出口）不受降级配置影响。
func (m *Models) wrapFailover(cfg *config.Config, log logx.Logger) {
	if m.Fallback != nil {
		wrap := wrapReactFailoverFn(cfg, m.Fallback, log)
		m.React = wrap(m.React, "chat")
		if m.R3 != nil {
			m.R3 = wrap(m.R3, plugin.LayerR3)
		}
		if m.QA != nil {
			m.QA = wrap(m.QA, "qa")
		}
		if m.Router != nil {
			m.Router = wrap(m.Router, "router")
		}
		if m.Reflect != nil {
			m.Reflect = wrap(m.Reflect, plugin.LayerR35)
		}
		// Reviewer ReAct 出口：有阶段覆盖时用覆盖链 RawModel（需 Failover 包）；
		// 无覆盖或覆盖链无 RawModel 时与主 React 出口同源（已包 Failover）。
		if m.ReviewerGen != nil {
			if rm, ok := m.ReviewerGen.(interface{ RawModel() model.BaseChatModel }); ok {
				if raw := rm.RawModel(); raw != nil {
					m.ReviewerReact = wrap(raw, "reviewer")
				}
			}
		}
	}
	if m.ReviewerReact == nil {
		m.ReviewerReact = m.React
	}
}

// wrapFit 直调出口统一包 Rate（限速最内层，v3.36.0）+ FitModel（输入两级拟合，
// 超限才动作、未超限零拷贝直通）+ ContinueModel（length 截断续写 ≤3 轮）。
// Generator 链已有 Client.fitInput，重叠路径双重拟合无副作用。
// 窗口取值：阶段覆盖 CONTEXT_TOKENS > 主配置 OPENAI_CONTEXT_TOKEN。
func (m *Models) wrapFit(cfg *config.Config, log logx.Logger) {
	fitOnEvent := func(stage string, res wrap.FitResult) {
		obs.IncrCtxFit(stage, string(res.Action))
		log.Warn("argus.ctxfit", "stage", stage, "action", string(res.Action),
			"before_runes", res.Before, "after_runes", res.After, "limit_runes", res.Limit,
			"cleared_tools", res.Cleared)
	}
	continueOnEvent := func(stage string, round, addedRunes int) {
		obs.IncrLLMContinued(stage)
		log.Warn("argus.llm.continued", "stage", stage, "round", round, "added_runes", addedRunes)
	}
	stageWindow := func(so *config.StageOverride) int {
		if so != nil && so.ContextTokens > 0 {
			return so.ContextTokens
		}
		return cfg.LLM.ContextToken
	}
	// 窗口配置防呆（v3.179.0 省 token 面）：FitModel 清理触发线 =(窗口-输出预留)
	// ×3/2×0.9（CJK 感知）——窗口虚高（如 flash 档配 1M）时触发线到百万级 runes，
	// 单请求上下文（工具预算合计 ~6 万 runes）永远到不了，microcompact 的「旧工具
	// 结果置占位」永不触发 → 工具结果随 ReAct 轮数线性膨胀（215 实测 role=tool
	// 累计 5000 万 runes = 上下文面 70%）。命中告警提示校正为模型真实窗口。
	const windowSanityTokens = 256_000
	if cfg.LLM.ContextToken > windowSanityTokens {
		log.Warn("argus.boot.window_suspect", "context_tokens", cfg.LLM.ContextToken,
			"hint", "窗口疑似虚高：FitModel 工具历史清理线将远超实际请求量级，"+
				"工具结果随轮数膨胀烧 token——按模型真实窗口校正 ARGUS_MODEL_CONTEXT_TOKENS")
	}
	wrapStage := func(bm model.BaseChatModel, stage string, window int) model.BaseChatModel {
		if bm == nil {
			return nil
		}
		// 限速最内层（v3.36.0）：直调旁路（ReAct/RawModel）与 Generator 链同桶，
		// ARGUS_LLM_RATE_LIMIT 此前对旁路无效的缺口闭合；Limiter nil = 直通。
		bm = wrap.NewRateModel(bm, m.Limiter)
		fm := wrap.NewFitModel(bm, stage, cfg.LLM.Model, window, cfg.MaxTokensResolved(), fitOnEvent)
		// v3.77.0 下沉后观测回调化：构成分解在装配点接 Prometheus（此前在实现内
		// 硬连 obs，SDK 不可携带 argus 指标）。
		fm.OnBreakdown = func(stage string, byRole map[string]int) {
			for role, n := range byRole {
				obs.AddCtxBreakdown(stage, role, n)
			}
		}
		// v5.5.3：重试环补在直调旁路最外层——ReAct（R3/Reviewer/R35/QA/Router）
		// 此前一发 429 即整轮阵亡（单轮侧早已有的韧性，这里对齐同参数）。
		return newRetryChatModel(wrap.NewContinueModel(fm, stage, continueOnEvent),
			cfg.LLM.LLMResilienceRetries+1, cfg.LLM.LLMResilienceBaseDelay,
			30*time.Second, stage, log)
	}
	m.React = wrapStage(m.React, "chat", cfg.LLM.ContextToken)
	if m.ReviewerGen == nil {
		m.ReviewerReact = m.React // 无 Reviewer 覆盖：与主 React 同源（含包装）
	}
	if m.R3 != nil {
		m.R3 = wrapStage(m.R3, plugin.LayerR3, stageWindow(cfg.StageModelR3))
	}
	if m.Reflect != nil {
		m.Reflect = wrapStage(m.Reflect, plugin.LayerR35, stageWindow(cfg.StageModelR35))
	}
	if m.QA != nil {
		m.QA = wrapStage(m.QA, "qa", stageWindow(cfg.StageModelQA))
	}
	if m.Router != nil {
		m.Router = wrapStage(m.Router, "router", stageWindow(cfg.StageModelRouter))
	}
	// Reviewer ReAct 出口仅在有独立覆盖链时另包（否则与 m.React 同源，已包装）
	if m.ReviewerGen != nil && m.ReviewerReact != nil {
		m.ReviewerReact = wrapStage(m.ReviewerReact, "reviewer", stageWindow(cfg.StageModelReviewer))
	}
}

// stageGenerator 阶段覆盖生成链：有全局降级 → Resilient 主备链（与主链共用
// fallback Provider）；否则单模型 Client（挂全局限速器）。
func stageGenerator(p llm.Provider, fallback llm.Provider, so *config.StageOverride,
	cfg *config.Config, limiter ratelimit.Limiter) (llm.Generator, error) {
	if fallback != nil {
		r, err := llm.NewResilient(llm.NewFallbackChain(p, fallback), llm.ResilientConfig{
			RetriesPerModel: cfg.LLM.LLMResilienceRetries, BaseDelay: cfg.LLM.LLMResilienceBaseDelay,
		})
		if err != nil {
			return nil, err
		}
		r.Limiter = limiter
		return r, nil
	}
	c := llm.NewClient(p.Model(), so.Model, nil)
	c.ContextTokens = so.ContextTokens
	if c.ContextTokens <= 0 {
		c.ContextTokens = cfg.LLM.ContextToken
	}
	c.MaxOutputTokens = cfg.MaxTokensResolved()
	// v5.5.2：同 buildGenerateChain——单模型 Client 也吃全局韧性参数。
	c.MaxRetries = cfg.LLM.LLMResilienceRetries + 1
	c.BaseDelay = cfg.LLM.LLMResilienceBaseDelay
	c.Limiter = limiter
	return c, nil
}

// wrapReactFailoverFn ReAct/直调出口的 FailoverModel 包装器（stage 名进切换日志）。
func wrapReactFailoverFn(cfg *config.Config, fallbackBM model.BaseChatModel,
	log logx.Logger) func(model.BaseChatModel, string) model.BaseChatModel {
	return func(primary model.BaseChatModel, stage string) model.BaseChatModel {
		if primary == nil {
			return nil
		}
		fm := llm.NewFailoverModel(
			llm.ChainLink{Model: primary, Name: cfg.LLM.Model},
			llm.ChainLink{Model: fallbackBM, Name: cfg.LLM.FallbackModel},
		)
		fm.OnFailover = func(from, to, reason string) {
			log.Warn("argus.llm.failover", "from", from, "to", to,
				"stage", stage, "reason", reason)
			obs.IncrLLMFailover(stage)
		}
		return fm
	}
}

// ptrNonNeg 正数取指针；0/负数返回 nil（不下发该参数）。
func ptrNonNeg(n int) *int {
	if n > 0 {
		t := n
		return &t
	}
	return nil
}
