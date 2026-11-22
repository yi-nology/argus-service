// Package outbound 出站 SSRF 守卫：按信任档校验目标 URL，并把拨号层守卫
// 挂到 HTTP 客户端（kernel netguard 的宿主接线层）。
//
// # 两个信任档
//
//   - 可信（Trusted）：配置/运维提供的地址（内部网关、IM、SIEM、控制台）。
//     允许私网与回环，但元数据、链路本地、unspecified 在任何档位都拒绝。
//   - 半可信（SemiTrusted）：repo 级配置与远端返回的地址（A2A Endpoint 与
//     card.URL）。只允许公网；私网、回环、链路本地、元数据默认拒绝。
//     ARGUS_OUTBOUND_ALLOW 是公网 host 白名单：只豁免名字层，**不放行私网**。
//
// # 模式
//
// ARGUS_OUTBOUND_MODE=off|warn|enforce，缺省 warn：记录违规但放行，首版不
// 改变线上行为；e2e 通过后切 enforce。off 完全旁路。
//
// # 逐跳
//
// HTTP 重定向必须重新校验（CheckRedirect 每跳调用同一判据）；拨号层守卫
// （netguard.DialFunc）闭合 DNS rebinding 窗口——解析与建连之间的 TOCTOU
// 由建连后核对 RemoteAddr 兜住。
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oekit/logx"
	"github.com/oagentkit/guardx/netguard"
)

// Mode 出站守卫模式。
type Mode string

const (
	// ModeOff 完全旁路（仅测试/紧急回滚用）。
	ModeOff Mode = "off"
	// ModeWarn 记录违规但放行（首版默认；不改变线上行为）。
	ModeWarn Mode = "warn"
	// ModeEnforce 拒绝违规出站。
	ModeEnforce Mode = "enforce"
)

const (
	envMode  = "ARGUS_OUTBOUND_MODE"
	envAllow = "ARGUS_OUTBOUND_ALLOW"

	// checkResolveTimeout 校验面 DNS 解析上限（与 kernel 校验面同值同因）。
	checkResolveTimeout = 10 * time.Second

	// maxRedirects 与 net/http 缺省口径一致（via 装已发出的请求）。
	maxRedirects = 10
)

// settings 一次配置的不可变快照；整体原子替换，避免读写撕裂。
type settings struct {
	mode  Mode
	allow map[string]struct{}
	log   logx.Logger
}

// defaultSettings 未显式配置时的缺省：warn（记录但放行）。
var defaultSettings = &settings{mode: ModeWarn}

var current atomic.Pointer[settings]

// loadSettings 取当前快照（永远非 nil）。
func loadSettings() *settings {
	if s := current.Load(); s != nil {
		return s
	}
	return defaultSettings
}

// Configure 设置全局模式与公网 host 白名单（测试入口）。
func Configure(mode Mode, allow []string, log logx.Logger) {
	m := Mode(strings.ToLower(strings.TrimSpace(string(mode))))
	switch m {
	case ModeOff, ModeWarn, ModeEnforce:
	default:
		if log != nil {
			log.Warn("argus.outbound.mode_invalid", "mode", string(mode), "fallback", string(ModeWarn))
		}
		m = ModeWarn
	}
	current.Store(&settings{mode: m, allow: normalizeAllow(allow), log: log})
}

// ConfigureFromEnv 从 ARGUS_OUTBOUND_MODE / ARGUS_OUTBOUND_ALLOW 装配全局守卫；
// bootstrap 装配早期调用。未配置时缺省 warn。
func ConfigureFromEnv(log logx.Logger) {
	mode := Mode(strings.TrimSpace(os.Getenv(envMode)))
	if mode == "" {
		mode = ModeWarn
	}
	var allow []string
	if raw := strings.TrimSpace(os.Getenv(envAllow)); raw != "" {
		allow = strings.Split(raw, ",")
	}
	Configure(mode, allow, log)
	eff := loadSettings()
	if log != nil {
		log.Info("argus.outbound.configured", "mode", string(eff.mode), "allow_hosts", len(eff.allow))
	}
}

// CheckSemiTrusted 校验半可信 URL：只允许 http/https 公网目标。
// warn 记录 argus.outbound.denied 后返回 nil；enforce 返回错误；off 旁路。
func CheckSemiTrusted(rawURL string) error {
	return checkSemiTrusted(loadSettings(), rawURL)
}

// tenantAllow 租户附加白名单（#96）：bootstrap 从 TenantProfiles 注册——
// tid → 该租户在全局白名单之外追加放行的公网 host。装配期一次性注册 +
// 运行期只读，RWMutex 足够（无锁-free CAS 的首存 nil 比较陷阱不踩）。
var (
	tenantAllowMu sync.RWMutex
	tenantAllow   = map[string]map[string]struct{}{}
)

// RegisterTenantAllow 登记/覆盖单租户的附加白名单（装配期调用；hosts 为
// 归一化 host 形态，与 ARGUS_OUTBOUND_ALLOW 同词表）。
func RegisterTenantAllow(tid string, hosts []string) {
	if tid == "" {
		return
	}
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		if h = strings.TrimSpace(h); h != "" {
			set[normalizeHost(h)] = struct{}{}
		}
	}
	tenantAllowMu.Lock()
	tenantAllow[tid] = set
	tenantAllowMu.Unlock()
}

// CheckSemiTrustedFor 租户感知半可信校验（#96）：租户附加白名单命中即豁免
// 名字层（地址族仍按 checkAllowlistedHost 逐 IP 复核——与全局白名单同一
// 安全语义，只是放行集按租户分）；未命中回落全局 CheckSemiTrusted。
func CheckSemiTrustedFor(tid, rawURL string) error {
	if tid != "" {
		tenantAllowMu.RLock()
		allow, ok := tenantAllow[tid]
		tenantAllowMu.RUnlock()
		if ok && len(allow) > 0 {
			u, err := parseHTTPURL(rawURL)
			if err != nil {
				return err
			}
			host := normalizeHost(u.Hostname())
			if _, hit := allow[host]; hit {
				return checkAllowlistedHost(host)
			}
		}
	}
	return CheckSemiTrusted(rawURL)
}

func checkSemiTrusted(s *settings, rawURL string) error {
	if s.mode == ModeOff {
		return nil
	}
	err := semiTrustedViolation(rawURL, s.allow)
	if err == nil {
		return nil
	}
	host := hostForLog(rawURL)
	if s.log != nil {
		s.log.Warn("argus.outbound.denied",
			"profile", "semi-trusted", "mode", string(s.mode), "host", host, "reason", err.Error())
	}
	if s.mode == ModeEnforce {
		return fmt.Errorf("出站守卫（半可信）拒绝 host %q: %w", host, err)
	}
	return nil
}

// semiTrustedViolation 返回「若 enforce 会拒绝」的原因；nil 表示放行。
// 判据单源 kernel netguard（名字层 + 解析后逐 IP 过 egress 策略）。
func semiTrustedViolation(rawURL string, allow map[string]struct{}) error {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	host := normalizeHost(u.Hostname())
	if _, ok := allow[host]; ok {
		return checkAllowlistedHost(host)
	}
	return netguard.CheckURLWith(rawURL, netguard.Allow{})
}

// checkAllowlistedHost 白名单只豁免名字层：地址族仍按零值 Allow 逐 IP 复核。
// v1 不因白名单放行私网/回环/链路本地/元数据——白名单是「公网 host 白名单」，
// 不是一个可以偷偷把地址级承诺升级成命名空间级承诺的开关。
func checkAllowlistedHost(host string) error {
	ips, err := resolveHost(host)
	if err != nil {
		return err
	}
	for _, ip := range ips {
		if err := netguard.CheckURLWith(ipLiteralURL(ip), netguard.Allow{}); err != nil {
			return fmt.Errorf("白名单 host %q 解析到受限地址 %s: %w", host, ip, err)
		}
	}
	return nil
}

// lookupNetIP 解析入口（测试可注入假解析器；生产走 net.DefaultResolver）。
var lookupNetIP = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

// resolveHost 解析 host（IP 字面量直接返回，域名走一次 DNS）。
func resolveHost(host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), checkResolveTimeout)
	defer cancel()
	addrs, err := lookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("出站目标解析失败（%s）: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("出站目标解析为空（%s）", host)
	}
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	return addrs, nil
}

// ipLiteralURL 把 IP 包成 netguard 可解析的 URL（IPv6 需方括号）。
func ipLiteralURL(ip netip.Addr) string {
	if ip.Is6() {
		return "http://[" + ip.String() + "]/"
	}
	return "http://" + ip.String() + "/"
}

// parseHTTPURL 只接受 http/https 且必须有 host。
// 解析错误不回显原始 URL（query 可能含 token）：统一 <invalid-url>。
func parseHTTPURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("出站 URL 非法（<invalid-url>）")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("出站 scheme %q 不在白名单（仅 http/https）", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("出站 URL 缺 host")
	}
	return u, nil
}

// normalizeAllow 归一化白名单条目（小写、去尾点、去端口/URL 外壳）。
func normalizeAllow(allow []string) map[string]struct{} {
	if len(allow) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(allow))
	for _, raw := range allow {
		h := normalizeAllowEntry(raw)
		if h != "" {
			out[h] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAllowEntry(raw string) string {
	h := strings.TrimSpace(strings.ToLower(raw))
	if h == "" {
		return ""
	}
	if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
		return normalizeHost(u.Hostname())
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.Trim(h, "[]")
	return normalizeHost(h)
}

// normalizeHost 小写 + 去尾点（DNS 大小写不敏感 + FQDN 尾点归一）。
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

// hostForLog 日志里只留 host（不带路径/查询串，避免把 token 写进日志）。
// 无 host / 解析失败统一 <invalid-url>，绝不回显原始 URL。
func hostForLog(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		return normalizeHost(u.Hostname())
	}
	return "<invalid-url>"
}
