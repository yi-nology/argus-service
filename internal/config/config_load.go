// config_load.go —— Load 的各段装载实现：核心/模型运维/LLM/向量/平台凭证。

package config

import (
	"os"
	"strings"
	"time"
)

// 上限派生基准（design §6 v3.1 修订）：模型窗口是唯一基准，全部上限按比例派生，
// 防止"换了 1M 模型服务仍按 128k 截断"的配置漂移。chars/token 按 2 保守估算
// （中文约 0.6~1 token/char，按 4 估会低估 token 数导致超窗 400）。

// loadCoreConfig 基础/服务/任务面配置段（环境变量字面量第一段）。
func loadCoreConfig() *Config {
	return &Config{
		Addr:                     envStr("ARGUS_ADDR", ":8899"),
		DBDSN:                    envStr("ARGUS_DB_DSN", "argus.db"),
		APIToken:                 os.Getenv("ARGUS_APITOKEN"),
		APITokens:                ParseAPITokens(os.Getenv("ARGUS_API_TOKENS")),
		BotUsername:              envStr("ARGUS_BOT_USERNAME", "argus-bot"),
		SkipDraft:                envBool("ARGUS_SKIP_DRAFT", true),
		MaxConcurrentTasks:       envInt("ARGUS_MAX_CONCURRENT_TASKS", 2),
		PluginMaxConcurrent:      envInt("ARGUS_PLUGIN_MAX_CONCURRENT", 2),
		MaxDiffChars:             envInt("ARGUS_MAX_DIFF_CHARS", 0),
		GetFileMaxRunesOverride:  envInt("ARGUS_GET_FILE_MAX_RUNES", 0),
		GetIssueMaxRunesOverride: envInt("ARGUS_GET_ISSUE_MAX_RUNES", 0),
		TaskTokenBudget:          envInt("ARGUS_TASK_TOKEN_BUDGET", 0),
		ReportMeta:               envBool("ARGUS_REPORT_META", true),
		ReportTemplate:           resolveReportTemplate(envStr("ARGUS_REPORT_TEMPLATE", "")),
		PIIMask:                  envBool("ARGUS_PII_MASK", true),
		PluginsFile:              envStr("ARGUS_PLUGINS", "plugins.yaml"),
		SkillsDir:                envStr("ARGUS_SKILLS_DIR", "skills"),
		WorkRoot:                 envStr("ARGUS_WORK_ROOT", "/tmp/argus-work"),
		KnowledgeDir:             envStr("ARGUS_KNOWLEDGE_DIR", "knowledge"),
		OcrBin:                   envStr("ARGUS_OCR_BIN", ""),
		OcrTimeout:               envDuration("ARGUS_OCR_TIMEOUT", 0),
		OcrHistoryDir:            envStr("ARGUS_OCR_HISTORY_DIR", ""),
		SASTGitleaksBin:          os.Getenv("ARGUS_SAST_GITLEAKS_BIN"),
		SASTSemgrepBin:           os.Getenv("ARGUS_SAST_SEMGREP_BIN"),
		SASTSemgrepConfig:        os.Getenv("ARGUS_SAST_SEMGREP_CONFIG"),
		SASTTimeout:              envDuration("ARGUS_SAST_TIMEOUT", 0),
		Codegraph:                envBool("ARGUS_CODEGRAPH", true),
		CodeSearch:               envBool("ARGUS_CODESEARCH", true),
		ImpactView:               envBool("ARGUS_IMPACTVIEW", true),
		ImpactCoupling:           envInt("ARGUS_IMPACT_COUPLING", 100),
		SemSearch:                envBool("ARGUS_SEMSEARCH", false),
		GraphPrecise:             envBool("ARGUS_GRAPH_PRECISE", false),
		EvalReportPath:           envStr("ARGUS_EVAL_REPORT", "eval/results/baseline-latest.json"),
		RepoAllowlist:            envList("ARGUS_REPO_ALLOWLIST"),
		APIRateRPM:               envInt("ARGUS_API_RATE_RPM", 120),
		AuditWebhookURL:          os.Getenv("ARGUS_AUDIT_WEBHOOK_URL"),
		AuditWebhookToken:        os.Getenv("ARGUS_AUDIT_WEBHOOK_TOKEN"),
		RelatedRepos:             envBool("ARGUS_RELATED_REPOS", false),
		TestGenVerifyCmd:         os.Getenv("ARGUS_TESTGEN_VERIFY_CMD"),
		FixVerifyCmd:             os.Getenv("ARGUS_FIX_VERIFY_CMD"),
		FixPush:                  fixPushEnabled(os.Getenv("ARGUS_FIX_PUSH")),
		FixDraft:                 envBool("ARGUS_FIX_DRAFT", false),
		FixPushMode:              fixPushMode(os.Getenv("ARGUS_FIX_PUSH")),
		FixAgent:                 os.Getenv("ARGUS_FIX_AGENT"),
		FixTimeout:               envDuration("ARGUS_FIX_TIMEOUT", 0),
		FixVerifyTimeout:         envDuration("ARGUS_FIX_VERIFY_TIMEOUT", 0),
		TestGenVerifyTimeout:     envDuration("ARGUS_TESTGEN_VERIFY_TIMEOUT", 0),
		Platforms:                map[string]PlatformConfig{},
		PollInterval:             envDuration("ARGUS_POLL_INTERVAL", 0),
		PollRepos:                os.Getenv("ARGUS_POLL_REPOS"),
		PublicURL:                strings.TrimRight(os.Getenv("ARGUS_PUBLIC_URL"), "/"),
		EnsureWebhook:            envBool("ARGUS_ENSURE_WEBHOOK", false),
		SDKCallTimeout:           envDuration("ARGUS_SDK_TIMEOUT", 0),
		TaskRetentionDays:        envInt("ARGUS_TASK_RETENTION_DAYS", 30),
		RepoMonthlyTokenBudget:   envInt("ARGUS_REPO_MONTHLY_TOKEN_BUDGET", 0),
	}
}

// applyModelOpsConfig 模型/预算/阶段/学习/评论/观测运维配置段（第二段字段回填）。
func applyModelOpsConfig(cfg *Config) {
	// 真实消费账月度上限（v6.9.0 P1-06，#11）：全部终态任务（含 failed/stale）的
	// LLM 消费聚合口径——失败/重试消费不再绕过硬预算；超限新任务失败终态告知。
	// 0 = 关。
	cfg.RealTokenBudget = envInt("ARGUS_REAL_TOKEN_BUDGET", 0)
	cfg.BudgetEnforcement = budgetEnforcement(envStr("ARGUS_BUDGET_ENFORCEMENT", "soft"))
	cfg.LLMCostPromptPer1K = envFloat("ARGUS_LLM_COST_PROMPT_PER_1K", 0)
	cfg.LLMCostCompletionPer1K = envFloat("ARGUS_LLM_COST_COMPLETION_PER_1K", 0)
	cfg.LLMCacheReadRatio = envFloat("ARGUS_LLM_COST_CACHE_READ_RATIO", 0.1)
	cfg.StageModelR1 = stageOverride("ARGUS_MODEL_R1")
	cfg.StageModelR2 = stageOverride("ARGUS_MODEL_R2")
	cfg.StageModelR3 = stageOverride("ARGUS_MODEL_R3")
	cfg.StageModelR35 = stageOverride("ARGUS_MODEL_R35")
	cfg.StageModelReviewer = stageOverride("ARGUS_REVIEWER_MODEL")
	cfg.StageModelQA = stageOverride("ARGUS_QA_MODEL")
	cfg.StageModelRouter = stageOverride("ARGUS_ROUTER_MODEL")
	cfg.ReflectRounds = envInt("ARGUS_REFLECTION_ROUNDS", 0)
	cfg.ReactMaxIterations = envInt("ARGUS_REACT_MAX_ITERATIONS", 12)
	cfg.CompletenessPolicy = normalizeCompletenessPolicy(envStr("ARGUS_COMPLETENESS_POLICY", "annotate"))
	cfg.CompletenessRetry = envBool("ARGUS_COMPLETENESS_RETRY", false)
	cfg.ReflectEvidence = normalizeEvidenceMode(envStr("ARGUS_REFLECTION_EVIDENCE", "off"))
	cfg.RegretLearning = envBool("ARGUS_REGRET_LEARNING", false)
	cfg.RegretMinSamples = envInt("ARGUS_REGRET_MIN_SAMPLES", 3)
	cfg.RegretSoftFloor = envFloat("ARGUS_REGRET_SOFT_FLOOR", 0.35)
	cfg.RegretHardFloor = envFloat("ARGUS_REGRET_HARD_FLOOR", 0.20)
	cfg.RegretInjectFloor = envFloat("ARGUS_REGRET_INJECT_FLOOR", 0.35)
	cfg.RegretInjectTopN = envInt("ARGUS_REGRET_INJECT_TOP_N", 10)
	cfg.DreamInterval = envDuration("ARGUS_DREAM_INTERVAL", 0)
	cfg.HealthReportInterval = envDuration("ARGUS_HEALTH_REPORT_INTERVAL", 0)
	cfg.CommentRouter = envBool("ARGUS_COMMENT_ROUTER", false)
	cfg.CommentTimeout = envDuration("ARGUS_COMMENT_TIMEOUT", 10*time.Minute)
	cfg.CommentConcurrency = envInt("ARGUS_COMMENT_CONCURRENCY", 2)
	cfg.TaskMaxRetries = envInt("ARGUS_TASK_MAX_RETRIES", 2)
	cfg.WorkerGrace = envDuration("ARGUS_WORKER_GRACE", 60*time.Second)
	cfg.MetricsAuth = envBool("ARGUS_METRICS_AUTH", false)
	cfg.EvalResultsPath = envStr("ARGUS_EVAL_RESULTS_PATH", "eval/results/latest.json")
	cfg.RouterMinConfidence = envFloat("ARGUS_ROUTER_MIN_CONFIDENCE", 0.6)
	cfg.CommentGate = envStr("ARGUS_COMMENT_GATE", "collaborator")
	cfg.LLMRateLimit = envFloat("ARGUS_LLM_RATE_LIMIT", 0)
	cfg.LLMRateBurst = envInt("ARGUS_LLM_RATE_BURST", 0)
	cfg.WebhookMaxBody = envInt("ARGUS_WEBHOOK_MAX_BODY", 1<<20)
	cfg.SinkURL = envStr("ARGUS_SINK_URL", "")
	cfg.SinkToken = envStr("ARGUS_SINK_TOKEN", "")
	cfg.OctopusURL = os.Getenv("ARGUS_OCTOPUS_URL")
	cfg.OctopusChannelID = int64(envInt("ARGUS_OCTOPUS_CHANNEL_ID", 0))
	cfg.OctopusUser = envStr("ARGUS_OCTOPUS_USER", "argus-bot")
	cfg.OctopusToken = os.Getenv("ARGUS_OCTOPUS_TOKEN")
	cfg.Debug = envBool("ARGUS_DEBUG", false)
	cfg.SkillMode = envStr("ARGUS_SKILL_MODE", "inject")
	cfg.MCPIdleReapInterval = envDuration("ARGUS_MCP_IDLE_REAP_INTERVAL", 5*time.Minute)
	cfg.MCPIdleReapMaxIdle = envDuration("ARGUS_MCP_IDLE_REAP_MAX_IDLE", 0)
	cfg.MemoryDir = envStr("ARGUS_MEMORY_DIR", "memory")
	cfg.MemoryKernel = envBool("ARGUS_MEMORY_KERNEL", false)
	cfg.MemoryFTSTokenize = envStr("ARGUS_MEMORY_FTS_TOKENIZE", "trigram")
	cfg.QAHistoryBudget = envInt("ARGUS_QA_HISTORY_BUDGET", 0)
	cfg.QAProgTools = envBool("ARGUS_QA_PTC", false)
	cfg.OTelEndpoint = envStr("ARGUS_OTEL_ENDPOINT", "")
	cfg.OTelProtocol = envStr("ARGUS_OTEL_PROTOCOL", "http")
	cfg.OTelInsecure = envBool("ARGUS_OTEL_INSECURE", true)
	cfg.OTelSampleRate = envFloat("ARGUS_OTEL_SAMPLE_RATE", 0)
	cfg.RepoGuideOff = envBool("ARGUS_REPO_GUIDE_OFF", false)
	cfg.SkillContentCap = envInt("ARGUS_SKILL_CONTENT_CAP", 100_000)
	cfg.ExternalToolCallLimit = envInt("ARGUS_EXTERNAL_TOOL_CALL_LIMIT", 5)
	cfg.MemoryIndexBudget = envInt("ARGUS_MEMORY_INDEX_BUDGET", 2000)
}

// applyLLMEmbeddingConfig 向量检索 + Embedding + LLM 子配置段（第三段回填）。
func applyLLMEmbeddingConfig(cfg *Config) {
	// Milvus 向量检索
	cfg.MilvusAddr = os.Getenv("ARGUS_MILVUS_ADDR")
	cfg.MilvusCollection = envStr("ARGUS_MILVUS_COLLECTION", "agentkit_knowledge")
	// Embedding
	cfg.EmbeddingBaseURL = os.Getenv("ARGUS_EMBEDDING_BASE_URL")
	cfg.EmbeddingAPIKey = os.Getenv("ARGUS_EMBEDDING_API_KEY")
	cfg.EmbeddingModel = envStr("ARGUS_EMBEDDING_MODEL", "text-embedding-v3")
	cfg.EmbeddingDim = envInt("ARGUS_EMBEDDING_DIM", 1024)
	cfg.LLM = LLMConfig{
		BaseURL:      os.Getenv("OPENAI_BASE_URL"),
		APIKey:       os.Getenv("OPENAI_API_KEY"),
		Model:        os.Getenv("OPENAI_MODEL"),
		Timeout:      envDuration("OPENAI_TIMEOUT", 300*time.Second),
		MaxTokens:    envInt("OPENAI_MAX_TOKENS", 0),
		ContextToken: envInt("ARGUS_MODEL_CONTEXT_TOKENS", 128_000),

		FallbackBaseURL:      os.Getenv("OPENAI_FALLBACK_BASE_URL"),
		FallbackAPIKey:       os.Getenv("OPENAI_FALLBACK_API_KEY"),
		FallbackModel:        os.Getenv("OPENAI_FALLBACK_MODEL"),
		FallbackContextToken: envInt("OPENAI_FALLBACK_CONTEXT_TOKENS", 128_000),

		LLMResilienceRetries:   envInt("ARGUS_LLM_RESILIENCE_RETRIES", 2),
		LLMResilienceBaseDelay: envDuration("ARGUS_LLM_RESILIENCE_BASE_DELAY", 2*time.Second),
	}
}

// applyPlatformCredentials 平台凭证同构装载 + GitHub App 覆盖（第四段回填）。
func applyPlatformCredentials(cfg *Config) {
	// 平台凭证同构装载：ARGUS_<大写平台>_{URL,TOKEN,WRITE_TOKEN,SECRET,SKIP_TLS,
	// TOKEN_STYLE,CONDITIONAL_REQUESTS,WEBHOOK_EVENTS}（词表 SupportedPlatforms；
	// tencent_code 的 env 前缀为 ARGUS_TENCENT_CODE_*）。
	for _, p := range SupportedPlatforms {
		prefix := "ARGUS_" + strings.ToUpper(p) + "_"
		cfg.Platforms[p] = PlatformConfig{
			BaseURL:             os.Getenv(prefix + "URL"),
			Token:               os.Getenv(prefix + "TOKEN"),
			WriteToken:          os.Getenv(prefix + "WRITE_TOKEN"),
			Secret:              os.Getenv(prefix + "SECRET"),
			SkipTLS:             envBool(prefix+"SKIP_TLS", false),
			TokenStyle:          os.Getenv(prefix + "TOKEN_STYLE"),
			ConditionalRequests: envBool(prefix+"CONDITIONAL_REQUESTS", true),
			WebhookEvents:       os.Getenv(prefix + "WEBHOOK_EVENTS"),
		}
	}
	// GitHub App 认证：三者全配时启用（覆盖 Token）
	if gh, ok := cfg.Platforms["github"]; ok {
		gh.GitHubAppID = int64(envInt("ARGUS_GITHUB_APP_ID", 0))
		gh.GitHubAppPrivateKey = os.Getenv("ARGUS_GITHUB_APP_PRIVATE_KEY")
		gh.GitHubAppInstallationID = int64(envInt("ARGUS_GITHUB_APP_INSTALLATION_ID", 0))
		cfg.Platforms["github"] = gh
	}
}
