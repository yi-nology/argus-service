// clients.go —— 出站守卫的客户端接线件。
//
// 两个信任档的拨号策略不同：
//   - 半可信：enforce 用 kernel netguard 严格档（名字层 + egress 拨号层）；
//     warn 只做记录（log-but-allow），不挂拨号层守卫；off 旁路。
//   - 可信：**不使用** netguard 名字层（其 .internal/.local/.lan 后缀无条件
//     拒绝，会误伤 argus.internal、octopus/SIEM 的 *.internal 端点），改为
//     自建拨号守卫：解析后逐 IP 校验，再拨已校验的 IP（TLS 仍用 URL host
//     作 SNI），建连后核对真实 RemoteAddr。
//
// 失败软着陆：base.Transport 是自定义 RoundTripper 时无法挂拨号层守卫，
// 记录 argus.outbound.guard_unavailable 并返回浅拷贝——不 panic；仍保留
// CheckRedirect 的 URL 级逐跳校验。
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"

	"github.com/oagentkit/guardx/netguard"
)

// trustedDialTimeout 可信档拨号上限（与 kernel 拨号层同值同因）。
const trustedDialTimeout = 10 * time.Second

// TrustedClient 返回可信档客户端：允许私网/回环（含 *.internal 等私有 DNS
// 名字），元数据/链路本地/unspecified 永远拒绝。
//
// 刻意绕过 kernel netguard 的名字层（见文件头）；ModeOff 完全旁路。
func TrustedClient(base *http.Client) *http.Client {
	s := loadSettings()
	if s.mode == ModeOff {
		return cloneClient(base)
	}
	c := cloneClient(base)
	next := baseCheckRedirect(base)
	tr, err := trustedTransport(base)
	if err != nil {
		logGuardUnavailable(s, "trusted", err)
	} else {
		c.Transport = installTrustedDial(tr)
	}
	c.CheckRedirect = trustedRedirect(s, next)
	return c
}

// SemiTrustedClient 返回半可信档客户端：
//   - enforce：netguard 零值 Allow（私网/回环/链路本地/元数据全拒）；
//   - warn：log-but-allow——不挂拨号层守卫（否则 link-local/元数据会被
//     netguard 硬拦，违背「记录但放行」语义），只由逐跳 CheckRedirect 与
//     调用点的 CheckSemiTrusted 留痕；
//   - off：原 client 浅拷贝，完全旁路。
func SemiTrustedClient(base *http.Client) *http.Client {
	s := loadSettings()
	if s.mode == ModeOff {
		return cloneClient(base)
	}
	next := baseCheckRedirect(base)
	if s.mode == ModeWarn {
		c := cloneClient(base)
		c.CheckRedirect = semiTrustedRedirect(s, next)
		return c
	}
	c, err := netguard.NewClient(base, netguard.Allow{})
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
		return nil // 域名：可信拨号守卫解析后逐 IP 判定
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

// trustedTransport 克隆 base 的传输层（nil 用 DefaultTransport 克隆）。
// 自定义 RoundTripper 无法挂拨号守卫，返回错误交给调用方失败软着陆。
func trustedTransport(base *http.Client) (*http.Transport, error) {
	var src *http.Transport
	if base != nil {
		switch tr := base.Transport.(type) {
		case nil:
		case *http.Transport:
			src = tr
		default:
			return nil, fmt.Errorf("客户端的 Transport 是自定义 RoundTripper（%T），无法挂可信档拨号守卫", tr)
		}
	}
	if src == nil {
		return http.DefaultTransport.(*http.Transport).Clone(), nil
	}
	return src.Clone(), nil
}

// installTrustedDial 挂上可信档拨号守卫。
func installTrustedDial(tr *http.Transport) *http.Transport {
	next := tr.DialContext
	// TLS 专用拨号会绕过本守卫，清掉；TLS 握手仍用请求 URL host 作 SNI。
	tr.DialTLSContext = nil
	//lint:ignore SA1019 清掉废弃的 TLS 拨号，确保 https 走本守卫
	tr.DialTLS = nil
	if next == nil {
		d := &net.Dialer{Timeout: trustedDialTimeout}
		next = d.DialContext
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return trustedDial(ctx, network, addr, next)
	}
	return tr
}

// trustedDial 可信档拨号：IP 字面量直接校验；域名解析后逐 IP 校验，再拨
// 已校验的 IP（不把域名交给系统拨号器，闭合 rebinding 窗口），建连后核对
// 真实 RemoteAddr 必须是刚校验的地址。
func trustedDial(ctx context.Context, network, addr string,
	next func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("拨号地址非法（<invalid-addr>）")
	}
	host = normalizeHost(host)
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		ip = ip.Unmap()
		if err := checkTrustedIP(ip, "拨号目标"); err != nil {
			return nil, err
		}
		return dialAndVerify(ctx, network, net.JoinHostPort(ip.String(), port), ip, next)
	}
	if isMetadataHostname(host) {
		return nil, fmt.Errorf("拨号主机名 %q 是云元数据地址（任何信任档均拒绝）", host)
	}
	ips, err := resolveTrustedHost(ctx, host)
	if err != nil {
		return nil, err
	}
	// 任一解析结果命中硬拒绝项即整名拒绝（不尝试其余地址）。
	for _, ip := range ips {
		if err := checkTrustedIP(ip, "拨号目标解析结果"); err != nil {
			return nil, err
		}
	}
	return raceDial(ctx, network, port, ips, next)
}

// dialAndVerify 拨单个已校验 IP，并核对真实对端地址。
func dialAndVerify(ctx context.Context, network, addr string, want netip.Addr,
	next func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	conn, err := next(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if err := checkTrustedRemoteAddr(conn, want); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

// resolveTrustedHost 解析可信档域名（失败/空结果 fail-closed）。
func resolveTrustedHost(ctx context.Context, host string) ([]netip.Addr, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, checkResolveTimeout)
	defer cancel()
	ips, err := lookupNetIP(resolveCtx, "ip", host)
	if err != nil {
		// 错误只含 host（DNS 错误串不含 URL/查询参数）。
		return nil, fmt.Errorf("可信出站目标解析失败（%s）: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("可信出站目标解析为空（%s）", host)
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap())
	}
	return out, nil
}

// checkTrustedRemoteAddr 建连后核对真实对端必须是已校验并拨号的 IP。
func checkTrustedRemoteAddr(conn net.Conn, want netip.Addr) error {
	if conn == nil {
		return nil
	}
	tcp, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil
	}
	got, ok := netip.AddrFromSlice(tcp.IP)
	if !ok {
		return nil
	}
	got = got.Unmap()
	if got != want {
		return fmt.Errorf("实际对端 %s 与已校验地址 %s 不符（任何信任档均拒绝）", got, want)
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
