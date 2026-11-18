// impactshare.go —— 影响面 share 链接令牌（v5.6.1）。
//
// MR 评论里的交互图链接要让评审人免 API 令牌直开，又不肯放宽整页鉴权：
// 每任务一枚 HMAC-SHA256(secret, taskID) 稳定令牌——恒定不过期（MR 评论
// 存活数月，短时令牌会让旧评论变死链），泄露面=单个任务的影响面页
// （其数据在 MR 评论里本已 mermaid 全量公开）。secret 取 API master token；
// 为空（鉴权关闭）时本机制整体不启用。

package impactview

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// shareDomain 分隔域，防令牌语义与其他 HMAC 用途混用。
const shareDomain = "argus-impact-share:"

// ImpactShareToken 任务绑定 share 令牌（hex；secret/taskID 空返回空串=机制未启用）。
// 租户域（#91）：非 default 租户把租户 ID 并入 HMAC 消息域——master token 泄露
// 不再能直接铸造全租户 share 令牌（须先拿到目标租户域知识）；default 租户
// 保持 legacy 形态（MR 评论里已发链接不失效）。
func ImpactShareToken(secret, tenant, taskID string) string {
	if secret == "" || taskID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(shareDomain + tenant + ":" + taskID))
	return hex.EncodeToString(m.Sum(nil))
}

// LegacyShareToken 单租户时代令牌形态（default 租户验证双形态兼容用）。
func LegacyShareToken(secret, taskID string) string {
	if secret == "" || taskID == "" {
		return ""
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(shareDomain + taskID))
	return hex.EncodeToString(m.Sum(nil))
}

// ImpactShareValid 常量时间校验（hmac.Equal，防时序侧信道）。tenant 为空 =
// default：为存量链接兼容，验证 legacy 与新形态双形态（迁移窗口）；非 default
// 只认新形态。
func ImpactShareValid(secret, tenant, taskID, got string) bool {
	if secret == "" || got == "" {
		return false
	}
	want := ImpactShareToken(secret, tenant, taskID)
	if tenant == "" || tenant == "default" {
		if legacy := LegacyShareToken(secret, taskID); legacy != "" &&
			hmac.Equal([]byte(legacy), []byte(got)) {
			return true
		}
	}
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(want), []byte(got))
}
