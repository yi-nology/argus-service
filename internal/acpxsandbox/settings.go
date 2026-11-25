// settings.go —— acpx 执行面的出站策略配置（部署级单例，装配早期一次性写入）。
package acpxsandbox

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/oagentkit/goagent-kernel/egress"
)

// Logger 本包需要的最小日志面：logx.Logger 与 service.Logger 都满足它。
// 不依赖完整 logx 接口——服务层有自己的窄 Logger，为一个日志面引入类型转换
// 只会把「谁在记日志」变成第二份真相。
type Logger interface {
	Info(msg string, keysAndValues ...any)
	Warn(msg string, keysAndValues ...any)
}

// 出站策略的环境变量面（与 execpolicy/outbound 同族：部署级开关 + 词表）。
const (
	envEgressMode   = "ARGUS_ACPX_EGRESS_MODE"
	envEgressProxy  = "ARGUS_ACPX_EGRESS_PROXY"
	envEgressAllow  = "ARGUS_ACPX_EGRESS_ALLOW"
	envEgressBypass = "ARGUS_ACPX_EGRESS_BYPASS"
)

// settings 一次配置的不可变快照；整体原子替换，避免读写撕裂。
type settings struct {
	spec egress.Spec
	err  error // 配置解析/自检失败（惰性到 New：让 acpx 插件在装配期显式停用）
	log  Logger
}

// defaultSettings 未配置时的缺省：unrestricted（与改动前的 acpx 行为一致）。
// 这不是「安全默认」而是「行为兼容默认」：受限档必须由部署方显式开启，且
// 开启后是否可兑现由 New/Run 判据决定，不会被这个零值悄悄放行。
var defaultSettings = &settings{spec: egress.Spec{Mode: egress.ModeUnrestricted}}

var current atomic.Pointer[settings]

// loadSettings 取当前快照（永远非 nil）。
func loadSettings() *settings {
	if s := current.Load(); s != nil {
		return s
	}
	return defaultSettings
}

// ConfigureFromEnv 从 ARGUS_ACPX_EGRESS_* 装配 acpx 出站策略。
// 未配置时缺省 unrestricted；非法值不回落缺省，而是把错误带到 New——acpx
// 插件会以明确理由停用（fail closed），不出现「配错了但照跑」的形态。
func ConfigureFromEnv(log Logger) {
	mode := strings.TrimSpace(os.Getenv(envEgressMode))
	proxy := strings.TrimSpace(os.Getenv(envEgressProxy))
	allow := splitList(os.Getenv(envEgressAllow))
	bypass := splitList(os.Getenv(envEgressBypass))
	Configure(mode, proxy, allow, bypass, log)
}

// Configure 设置全局出站策略（测试入口与 ConfigureFromEnv 共用同一条解析线）。
func Configure(mode, proxy string, allow, bypass []string, log Logger) {
	m, err := parseEgressMode(mode)
	s := &settings{
		spec: egress.Spec{Mode: m, Proxy: proxy, Allow: allow, Bypass: bypass},
		log:  log,
	}
	if err == nil {
		s.err = s.spec.Validate()
	} else {
		s.err = err
	}
	current.Store(s)
	if log != nil {
		if s.err != nil {
			log.Warn("argus.acpxsandbox.egress_invalid", "mode", mode, "error", s.err.Error())
			return
		}
		log.Info("argus.acpxsandbox.egress_configured", "mode", s.spec.Mode.String(),
			"proxy", s.spec.Proxy, "allow", len(s.spec.Allow), "bypass", len(s.spec.Bypass))
	}
}

// parseEgressMode 归一出口档位词表；空串 = unrestricted（显式缺省）。
func parseEgressMode(raw string) (egress.Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "unrestricted":
		return egress.ModeUnrestricted, nil
	case "proxy":
		return egress.ModeProxy, nil
	case "denied":
		return egress.ModeDenied, nil
	default:
		return 0, fmt.Errorf("acpxsandbox: 未知出站档位 %q（unrestricted|proxy|denied）", raw)
	}
}

// splitList 逗号分隔名单（空白项丢弃、保序）。
func splitList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
