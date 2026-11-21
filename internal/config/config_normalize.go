// config_normalize.go —— 配置值归一/白名单解析与告警日志口。

package config

import (
	"strings"

	"github.com/yi-nology/argus/internal/reporttmpl"
)

// env 读取助手：类型解析复用 oekit/core/config（strconv 同源），非法值回退默认时
// 保留 warnBadEnv 留痕——oekit/core/config 的 *WithDefault 系列是静默回退，不能直接换。

// resolveReportTemplate ARGUS_REPORT_TEMPLATE 白名单解析（v6.10.0）：词表单源
// reporttmpl；空/非法值回退 standard（模板是渲染+提示词契约变更，错误默认比
// 缺失更有害，宁可保守；想要非 standard 必须显式给出合法值）。按仓配置优先
// 级裁决在 reporttmpl.Resolve。
func resolveReportTemplate(v string) string {
	return reporttmpl.Resolve("", v)
}

// fixPushMode ARGUS_FIX_PUSH 值解析（v7.0 Verified Fix Loop）：
// "1"/"branch" → "branch"（修复分支+PR 形态，v7.0 缺省）；"direct" → "direct"
// （v3.74.1 直推旧形态）；其余（含空/"0"）→ ""（环关闭，与旧 `== "1"` 兼容）。
func fixPushMode(v string) string {
	switch v {
	case "1", "branch":
		return "branch"
	case "direct":
		return "direct"
	case "confirm":
		// T4 用户面两阶段：验证+审批后不直推，落 await_confirm，
		// 由 POST /fix-push/confirm 绑定 expected_remote_head 后执行。
		return "confirm"
	default:
		return ""
	}
}

// fixPushEnabled 环开关（mode 非空即开）。
func fixPushEnabled(v string) bool { return fixPushMode(v) != "" }

// budgetEnforcement 预算强制模式归一：仅 "hard"（大小写不敏感、去空白）为
// hard，其余（含空/非法值）一律 soft——非法值不阻断启动，缺省可用性优先。
func budgetEnforcement(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "hard") {
		return "hard"
	}
	return "soft"
}

// WarnLogger WarnOnly 的日志口（与 logx.Logger 解耦，测试可注入）。
type WarnLogger interface{ Warn(msg string, kv ...any) }
