// config_tenant_profile.go —— 每租户配置覆盖（#95/#96/#97/#101/#90 的配置面）。
//
// ARGUS_TENANT_PROFILES 指向 JSON 文件：{"tenants": {"<id>": {...}}}。租户
// profile 只放「按租户合法不同」的覆盖项——平台凭证/LLM 网关/Bot 身份/旁路
// 出口/出站附加白名单；未覆盖项回落全局默认（单租户部署零变化：不配置本
// 文件即全 default）。信任分层红线不变：profile 是运维可信面（与 env 同级），
// 仓库侧 .reviewagents.yaml 仍是半可信收窄面。
//
// 消费纪律：消费点用 ProfileFor(tenant.FromContext(ctx)) 取有效 profile，
// 严禁绕过 resolver 直读 Tenants 表（缺省回落必须在单点）。

package config

import (
	"encoding/json"
	"os"
)

// TenantProfile 单租户覆盖集。空字段 = 用全局默认（结构即差量）。
type TenantProfile struct {
	// BotUsername 该租户回帖身份（自回环抑制/报告头校验按租户隔离）。
	BotUsername string `json:"bot_username,omitempty"`
	// LLM 该租户独立网关/密钥/模型（计费与配额隔离的配置前提，#95）。
	LLM TenantLLM `json:"llm,omitempty"`
	// Sink 审查结果承接端点按租户路由（#97）。
	Sink TenantEndpoint `json:"sink,omitempty"`
	// Octopus IM 通知按租户路由（#97）。
	Octopus TenantEndpoint `json:"octopus,omitempty"`
	// AuditWebhook 审计事件按租户路由（#97）。
	AuditWebhook TenantEndpoint `json:"audit_webhook,omitempty"`
	// OutboundAllowExtra 该租户在全局白名单之外追加放行的主机（#96）。
	OutboundAllowExtra []string `json:"outbound_allow_extra,omitempty"`
	// RealTokenBudget 租户级真实消费账月度上限（#93；0=继承全局）：租户 A
	// 超限只熔断租户 A 的新任务，不再全局株连——全局标量是残余熔断面的根治。
	RealTokenBudget int `json:"real_token_budget,omitempty"`
	// Platforms 该租户独立平台凭证（base_url/token/secret；#90——凭证串用
	// 根治的配置前提）。键 = 平台名。
	Platforms map[string]PlatformConfig `json:"platforms,omitempty"`
}

// TenantLLM 租户级 LLM 覆盖（空字段回落全局 LLMConfig 对应项）。
type TenantLLM struct {
	BaseURL string `json:"base_url,omitempty"`
	APIKey  string `json:"api_key,omitempty"`
	Model   string `json:"model,omitempty"`
}

// TenantEndpoint 单端点覆盖（URL/Token 成对）。
type TenantEndpoint struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// tenantProfilesFile ARGUS_TENANT_PROFILES 文件形态。
type tenantProfilesFile struct {
	Tenants map[string]TenantProfile `json:"tenants"`
}

// LoadTenantProfiles 装载租户 profile 文件（路径空/文件缺失/坏 JSON = 空表
// + 留痕——profile 是增强面，装载失败不得拖垮启动；调用方回落全局默认）。
func LoadTenantProfiles(path string) map[string]TenantProfile {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		warnBadEnv("ARGUS_TENANT_PROFILES", path, "profile 文件不可读，按无租户覆盖继续")
		return nil
	}
	var f tenantProfilesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		warnBadEnv("ARGUS_TENANT_PROFILES", path, "profile 文件非法 JSON: "+err.Error())
		return nil
	}
	for id := range f.Tenants {
		if NormalizeTenantID(id) == "" {
			warnBadEnv("ARGUS_TENANT_PROFILES", id, "租户 ID 非法（空/含空白），条目忽略")
			delete(f.Tenants, id)
		}
	}
	return f.Tenants
}

// NormalizeTenantID 租户 ID 白名单校验：非空、无空白、无路径分隔符——
// ID 会进缓存键/文件路径段（t-<id>/ 形态），装载期消毒一次，消费侧零判。
func NormalizeTenantID(id string) string {
	if id == "" {
		return ""
	}
	for _, r := range id {
		switch {
		case r == '/', r == '\\', r == 0:
			return ""
		case r == ' ', r == '\t', r == '\n', r == '\r':
			return ""
		}
	}
	return id
}

// ProfileFor 租户有效 profile（缺省回落全局默认的差量语义由消费点逐字段
// 处理——profile 即差量，resolver 只保证「拿到该租户的差量」）。
func (c *Config) ProfileFor(tid string) TenantProfile {
	if c == nil || c.TenantProfiles == nil {
		return TenantProfile{}
	}
	return c.TenantProfiles[tid]
}
