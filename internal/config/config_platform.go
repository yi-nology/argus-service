// config_platform.go —— git 平台接入配置（凭证、写 token 回落、GitHub App）。

package config

// PlatformConfig 单个 git 平台的接入凭证。
type PlatformConfig struct {
	BaseURL string
	Token   string
	Secret  string // webhook 签名密钥（GitLab 是静态 token 语义，SDK 已屏蔽差异）
	// SkipTLS 跳过 TLS 证书校验（内网自建实例自签/过期证书场景，如 git.example.com）。
	// 仅影响平台 API 与工作副本克隆；开启时日志告警提示风险。
	SkipTLS bool
	// TokenStyle 覆盖默认认证头风格: "bearer" (Authorization: Bearer) / "private" (PRIVATE-TOKEN)。
	// 空值使用平台默认值（GitLab=private, GitHub=bearer）。
	TokenStyle string
	// WriteToken 写凭证分离（Phase 3 Fix v2，spec §4）：读 Token / 写 WriteToken
	// 分离的写侧（ARGUS_<PLATFORM>_WRITE_TOKEN；缺省回落 Token=读写同凭证）。
	// 消费面=workcopy CredentialOf（clone/fetch 读）与修复链 git push（写）——
	// 最小权限：只读 token 不再被迫承担 push。
	WriteToken string
	// ConditionalRequests 开启 GET 条件请求（ETag/If-None-Match，304 重放缓存体）：
	// v5.4.0 起默认开（上游 go-git-platform v0.100.2 修掉 304×LRU 驱逐竞态的硬失败，
	// 进程内自愈后可安全默认）——GitHub 的 304 不计入限流预算，poller 每 tick 全量
	// 翻页场景可持续轮询速率实质抬升；平台不支持 ETag 时透明回退 200，无正确性影响。
	// ARGUS_<PLATFORM>_CONDITIONAL_REQUESTS=0 显式关闭。
	ConditionalRequests bool
	// WebhookEvents 覆盖启动期 webhook 确保的事件集（ARGUS_<P>_WEBHOOK_EVENTS，
	// 逗号分隔；空 = 用 bootstrap 内置的平台原生默认词表）。词表是平台原生的
	//（SDK 后端对 CreateWebhookOptions.Events 原样透传或按族映射，各平台不同）。
	WebhookEvents string

	// GitHub App 认证（仅 github 平台）：配置后自动用 installation token 替代 PAT。
	// 三者全配才启用；AppID + PrivateKey 用于签发 JWT，InstallationID 用于换取 token。
	GitHubAppID             int64  // ARGUS_GITHUB_APP_ID
	GitHubAppPrivateKey     string // ARGUS_GITHUB_APP_PRIVATE_KEY（PEM 内容或 base64 编码）
	GitHubAppInstallationID int64  // ARGUS_GITHUB_APP_INSTALLATION_ID
}

// WriteTokenOf 平台写凭证（Phase 3 写凭证分离单源）：WriteToken 缺省回落
// Token（读写同凭证形态保持零配置兼容）。
func (pc PlatformConfig) WriteTokenOf() string {
	if pc.WriteToken != "" {
		return pc.WriteToken
	}
	return pc.Token
}
