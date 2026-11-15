// Package config 承载 Argus 全部运行配置（design §6/§12）。
// 环境变量是唯一配置来源；plugins.yaml 只声明插件清单，不承载服务配置。
package config

// SupportedPlatforms 支持的 git 平台词表（单一事实源）：env 装载（ARGUS_<大写平台>_*）、
// 启动校验提示、webhook 事件词表完备性、MCP 工具描述口径、部署文档全部以此为准。
// 与上游 go-git-platform 注册后端集合的对齐由 config 防漂移测试把关（SDK 增删平台
// 即门禁红，强迫人工决策）；新增平台须同步 webhook_ensure 事件词表、mcpserver
// platformVocab 与 skills/argus-mcp/references/tools.md。
// gitcode / 腾讯工蜂（tencent_code）：SDK 后端已注册，Argus 端未实测（文档已标注）。
var SupportedPlatforms = []string{"gitea", "forgejo", "github", "gitlab", "gitee", "gitcode", "tencent_code"}

func Load() *Config {
	cfg := loadCoreConfig()
	applyModelOpsConfig(cfg)
	applyLLMEmbeddingConfig(cfg)
	applyPlatformCredentials(cfg)
	return cfg
}
