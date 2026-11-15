// clients.go —— 出站守卫的客户端接线件：把 kernel netguard 的拨号层守卫挂到
// 宿主的 HTTP 客户端上，并按信任档设置逐跳重定向复验。
//
// 失败软着陆（brief 要求）：base.Transport 若是自定义 RoundTripper，
// netguard.NewClient 无法挂拨号层守卫（拨号发生在传输层内部）。此时记录
// argus.outbound.guard_unavailable 并返回原 client 的浅拷贝——**不 panic**；
// 仍保留 CheckRedirect 的 URL 级逐跳校验（写面早失败仍有效）。
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"

	"github.com/yi-nology/goagent-kernel/netguard"
)

// TrustedClient 返回可信档客户端：允许私网/回环，元数据/链路本地/unspecified
// 永远拒绝（重定向逐跳复验 + 拨号层复核）。
// base 为 nil 时按零值 client 派生；base 的 Transport 必须是 *http.Transport。
func TrustedClient(base *http.Client) *http.Client {
	s := loadSettings()
	next := baseCheckRedirect(base)
	c, err := netguard.NewClient(base, netguard.AllowSelfService())
	if err != nil {
		logGuardUnavailable(s, "trusted", err)
		c = cloneClient(base)
	} else {
		addMetadataDialGuard(c)
	}
	c.CheckRedirect = trustedRedirect(s, next)
	return c
}

// SemiTrustedClient 返回半可信档客户端：enforce 用零值 Allow（私网/回环/
// 链路本地/元数据全拒）；warn 用 AllowSelfService 放行但由 CheckRedirect /
// CheckSemiTrusted 记录违规；off 返回原 client（不改变行为）。
func SemiTrustedClient(base *http.Client) *http.Client {
	s := loadSettings()
	if s.mode == ModeOff {
		return cloneClient(base)
	}
	allow := netguard.Allow{}
	if s.mode == ModeWarn {
		allow = netguard.AllowSelfService()
	}
	next := baseCheckRedirect(base)
	c, err := netguard.NewClient(base, allow)
	if err != nil {
		logGuardUnavailable(s, "semi-trusted", err)
		c = cloneClient(base)
	}
	c.CheckRedirect = semiTrustedRedirect(s, next)
	return c
}

// semiTrustedRedirect 每跳调用同一判据；warn 在 checkSemiTrusted 内记录后放行。
func semiTrustedRedirect(s *settings, next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("重定向超过 %d 跳", maxRedirects)
		}
		if err := checkSemiTrusted(s, req.URL.String()); err != nil {
			return err
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
}

// trustedRedirect 可信档逐跳复验：元数据/链路本地/unspecified 永远拒绝
// （私网与回环放行，这是可信档与半可信档的唯一差别）。
func trustedRedirect(s *settings, next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("重定向超过 %d 跳", maxRedirects)
		}
		if err := checkTrustedURL(req.URL); err != nil {
			if s.log != nil {
				s.log.Warn("argus.outbound.denied",
					"profile", "trusted", "hop", len(via)+1,
					"host", normalizeHost(req.URL.Hostname()), "reason", err.Error())
			}
			return err
		}
		if next != nil {
			return next(req, via)
		}
		return nil
	}
}

// checkTrustedURL 可信档 URL 判据：scheme + 元数据/链路本地/unspecified 硬拒绝。
func checkTrustedURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("出站 URL 为空")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("出站 scheme %q 不在白名单（仅 http/https）", u.Scheme)
	}
	host := normalizeHost(u.Hostname())
	if host == "" {
		return fmt.Errorf("出站 URL 缺 host")
	}
	if isMetadataHostname(host) {
		return fmt.Errorf("出站主机名 %q 是云元数据地址（任何信任档均拒绝）", host)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return nil // 域名：拨号层 netguard 名字/解析判据负责
	}
	ip = ip.Unmap()
	if isMetadataIP(ip) {
		return fmt.Errorf("出站地址 %s 是云元数据地址（任何信任档均拒绝）", ip)
	}
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("出站地址 %s 是链路本地地址（任何信任档均拒绝）", ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("出站地址 %s 是未指定地址（任何信任档均拒绝）", ip)
	}
	return nil
}

// addMetadataDialGuard 在 netguard 拨号守卫之外再补一层元数据硬拒绝。
// netguard 的 egress 策略把 fd00:ec2::254 归入 ULA 私网（AllowSelfService
// 会放行），而可信档要求元数据地址永远拒绝——这一层补三个缺口：
//   - 字面量拨号地址（含 IPv4-mapped）建连前拒绝；
//   - 域名解析结果显式含元数据地址时建连前拒绝；
//   - 建连后核对真实 RemoteAddr（DNS rebinding 换到元数据地址同样拒绝）。
func addMetadataDialGuard(c *http.Client) {
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		return
	}
	next := tr.DialContext
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if err := checkTrustedDialAddr(ctx, addr); err != nil {
			return nil, err
		}
		conn, err := next(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		if err := checkTrustedRemoteAddr(conn); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	}
}

// checkTrustedDialAddr 对拨号地址做元数据/链路本地/unspecified 硬拒绝：
// 字面量直接判；域名先解析一次（解析失败交给 netguard 的解析层报错）。
func checkTrustedDialAddr(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil // 交给 netguard 报「拨号地址非法」
	}
	host = normalizeHost(host)
	if ip, err := netip.ParseAddr(host); err == nil {
		return checkTrustedIP(ip.Unmap(), "拨号目标")
	}
	if isMetadataHostname(host) {
		return fmt.Errorf("拨号主机名 %q 是云元数据地址（任何信任档均拒绝）", host)
	}
	resolveCtx, cancel := context.WithTimeout(ctx, checkResolveTimeout)
	defer cancel()
	ips, lookupErr := lookupNetIP(resolveCtx, "ip", host)
	if lookupErr != nil {
		return nil // 解析失败由 netguard 报（不在这里改变错误契约）
	}
	for _, ip := range ips {
		if err := checkTrustedIP(ip.Unmap(), "拨号目标解析结果"); err != nil {
			return err
		}
	}
	return nil
}

// checkTrustedRemoteAddr 建连后核对真实对端：元数据地址一律断开。
// netguard 已核对 egress 策略（链路本地/私网等），这里只补它按「私网」
// 放行的元数据 ULA。
func checkTrustedRemoteAddr(conn net.Conn) error {
	if conn == nil {
		return nil
	}
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	ip, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return nil
	}
	ip = ip.Unmap()
	if isMetadataIP(ip) {
		return fmt.Errorf("实际对端 %s 是云元数据地址（任何信任档均拒绝）", ip)
	}
	return nil
}

// checkTrustedIP 可信档 IP 硬拒绝项（私网/回环/公网放行）。
func checkTrustedIP(ip netip.Addr, where string) error {
	if isMetadataIP(ip) {
		return fmt.Errorf("%s %s 是云元数据地址（任何信任档均拒绝）", where, ip)
	}
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%s %s 是链路本地地址（任何信任档均拒绝）", where, ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%s %s 是未指定地址（任何信任档均拒绝）", where, ip)
	}
	return nil
}

// isMetadataHostname 云元数据主机名（精确名单，归一化比较）。
func isMetadataHostname(host string) bool {
	switch host {
	case "metadata.google.internal", "metadata.goog":
		return true
	}
	return false
}

// metadataIPs 云元数据地址（三种主流形态 + 阿里云 100.100.100.200）。
// 169.254.169.254 同时是链路本地，这里显式列出是为了错误信息可归因。
var metadataIPs = []netip.Addr{
	netip.MustParseAddr("169.254.169.254"),
	netip.MustParseAddr("fd00:ec2::254"),
	netip.MustParseAddr("100.100.100.200"),
}

func isMetadataIP(ip netip.Addr) bool {
	for _, m := range metadataIPs {
		if ip == m {
			return true
		}
	}
	return false
}

// baseCheckRedirect 保留调用方原有的 CheckRedirect（我们的判据跑在前）。
func baseCheckRedirect(base *http.Client) func(*http.Request, []*http.Request) error {
	if base == nil {
		return nil
	}
	return base.CheckRedirect
}

// cloneClient 浅拷贝（不修改调用方的 client；nil 时给零值 client）。
func cloneClient(base *http.Client) *http.Client {
	if base == nil {
		return &http.Client{}
	}
	c := *base
	return &c
}

func logGuardUnavailable(s *settings, profile string, err error) {
	if s.log != nil {
		s.log.Warn("argus.outbound.guard_unavailable", "profile", profile, "error", err.Error())
	}
}
