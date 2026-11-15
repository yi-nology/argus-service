package outbound

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/oekit/logx"
)

// TestTrustedClientLoopback httptest 跑在回环：可信档必须能连上。
func TestTrustedClientLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := TrustedClient(&http.Client{Timeout: 2 * time.Second})
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("可信档应放行回环: %v", err)
	}
	_ = resp.Body.Close()
}

// TestTrustedRedirectProfile 可信档：私网/回环放行；元数据/链路本地/
// unspecified 任何模式都拒绝。
func TestTrustedRedirectProfile(t *testing.T) {
	resetSettings(t)
	lg := newRecordLogger()
	Configure(ModeWarn, nil, lg) // warn 也不能放宽可信档硬拒绝
	c := TrustedClient(&http.Client{})
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)}

	for _, raw := range []string{"http://127.0.0.1/", "http://10.0.0.1/", "http://192.168.1.1/"} {
		if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, raw, nil), via); err != nil {
			t.Fatalf("可信档应放行 %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"http://169.254.169.254/", "http://[fd00:ec2::254]/",
		"http://metadata.google.internal/", "http://[fe80::1]/", "http://0.0.0.0/",
	} {
		if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, raw, nil), via); err == nil {
			t.Fatalf("可信档应拒绝 %s", raw)
		}
	}
	if lg.first() != "argus.outbound.denied" {
		t.Fatalf("可信档违规应记录: %v", lg.warns)
	}
	// 跳数上限同样生效。
	if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil),
		make([]*http.Request, maxRedirects)); err == nil {
		t.Fatal("超过跳数上限应拒绝")
	}
}

// TestTrustedClientAllowsInternalName 可信档必须放行私有 DNS 名字
// （kernel netguard 名字层会无条件拒绝 .internal，本档自建拨号守卫绕过它）。
func TestTrustedClientAllowsInternalName(t *testing.T) {
	resetSettings(t)
	Configure(ModeWarn, nil, logx.Nop())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if normalizeHost(host) != "argus.internal" {
			return nil, fmt.Errorf("unexpected host %q", host)
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}

	c := TrustedClient(&http.Client{Timeout: 2 * time.Second})
	resp, err := c.Get("http://argus.internal:" + u.Port() + "/")
	if err != nil {
		t.Fatalf("可信档应放行 .internal 私有 DNS 名字: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "ok" {
		t.Fatalf("响应不符: %q", body)
	}
}

// TestTrustedClientDeniesMetadata 可信档元数据地址永远拒绝（字面量 + 解析结果）。
func TestTrustedClientDeniesMetadata(t *testing.T) {
	resetSettings(t)
	Configure(ModeWarn, nil, logx.Nop())
	c := TrustedClient(&http.Client{Timeout: time.Second})
	for _, raw := range []string{
		"http://169.254.169.254/", "http://[fd00:ec2::254]/", "http://metadata.google.internal/",
	} {
		if _, err := c.Get(raw); err == nil {
			t.Fatalf("%s 应拒绝", raw)
		}
	}

	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fd00:ec2::254")}, nil
	}
	if _, err := c.Get("http://evil.example/"); err == nil {
		t.Fatal("域名解析到元数据 ULA 应拒绝")
	}
}

// TestTrustedClientOffBypasses ModeOff 必须旁路可信档守卫。
func TestTrustedClientOffBypasses(t *testing.T) {
	resetSettings(t)
	Configure(ModeOff, nil, logx.Nop())
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}
	c := TrustedClient(base)
	if c.CheckRedirect != nil {
		t.Fatal("off 应保留 base 默认 CheckRedirect（nil）")
	}
	resp, err := c.Get("http://169.254.169.254/")
	if err != nil {
		t.Fatalf("off 应旁路可信档守卫: %v", err)
	}
	_ = resp.Body.Close()
}

// TestTrustedDialClassifies 可信档拨号判据（注入解析器 + 桩拨号器）。
func TestTrustedDialClassifies(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })

	next := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial should not be reached")
	}
	// 字面量硬拒绝项在拨号前拒绝。
	for _, addr := range []string{
		"169.254.169.254:80", "[fd00:ec2::254]:443", "[::ffff:169.254.169.254]:80",
		"100.100.100.200:80", "[fe80::1]:80", "0.0.0.0:80", "metadata.google.internal:80",
	} {
		if _, err := trustedDial(context.Background(), "tcp", addr, next); err == nil {
			t.Fatalf("%s 应拒绝", addr)
		}
	}
	if _, err := trustedDial(context.Background(), "tcp", "bad-addr", next); err == nil {
		t.Fatal("非法拨号地址应拒绝")
	}

	// 域名解析到元数据/链路本地：整名拒绝（不尝试其它地址）。
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fd00:ec2::254")}, nil
	}
	if _, err := trustedDial(context.Background(), "tcp", "evil.example:443", next); err == nil {
		t.Fatal("域名解析到元数据 ULA 应拒绝")
	}
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("dns failure")
	}
	if _, err := trustedDial(context.Background(), "tcp", "broken.example:443", next); err == nil {
		t.Fatal("解析失败应 fail-closed")
	}
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, nil
	}
	if _, err := trustedDial(context.Background(), "tcp", "empty.example:443", next); err == nil {
		t.Fatal("空解析结果应 fail-closed")
	}

	// 私网解析结果允许：拨号器收到的是已校验的 IP，且 RemoteAddr 必须匹配。
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1")}, nil
	}
	fc := &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1")}}
	gotConn, err := trustedDial(context.Background(), "tcp", "gateway.internal:8443",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr != "10.0.0.1:8443" {
				t.Fatalf("应拨已校验 IP，got %s", addr)
			}
			return fc, nil
		})
	if err != nil || gotConn != fc {
		t.Fatalf("私网域名应放行: conn=%v err=%v", gotConn, err)
	}
	// 字面量私网/回环/公网放行。
	for _, addr := range []string{"10.0.0.1:80", "127.0.0.1:80", "1.1.1.1:443"} {
		want := addr[:strings.LastIndex(addr, ":")]
		fc := &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(want)}}
		if _, err := trustedDial(context.Background(), "tcp", addr,
			func(context.Context, string, string) (net.Conn, error) { return fc, nil }); err != nil {
			t.Fatalf("%s 应放行: %v", addr, err)
		}
	}
}

func TestTrustedDialRemoteAddrMismatch(t *testing.T) {
	fc := &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2")}}
	_, err := trustedDial(context.Background(), "tcp", "10.0.0.1:80",
		func(context.Context, string, string) (net.Conn, error) { return fc, nil })
	if err == nil {
		t.Fatal("实际对端与已校验地址不符应拒绝")
	}
	if !fc.closed {
		t.Fatal("被拒连接必须关闭")
	}
}

// remoteConn 只覆写 RemoteAddr（建连后地址核对用）。
type remoteConn struct {
	net.Conn
	addr net.Addr
}

func (c remoteConn) RemoteAddr() net.Addr { return c.addr }

func TestCheckTrustedRemoteAddr(t *testing.T) {
	want := netip.MustParseAddr("10.0.0.1")
	if err := checkTrustedRemoteAddr(nil, want); err != nil {
		t.Fatalf("nil conn 应安全: %v", err)
	}
	ok := remoteConn{addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1")}}
	if err := checkTrustedRemoteAddr(ok, want); err != nil {
		t.Fatalf("匹配地址应放行: %v", err)
	}
	bad := remoteConn{addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.2")}}
	if err := checkTrustedRemoteAddr(bad, want); err == nil {
		t.Fatal("地址不符应拒绝")
	}
	unix := remoteConn{addr: &net.UnixAddr{Name: "x", Net: "unix"}}
	if err := checkTrustedRemoteAddr(unix, want); err != nil {
		t.Fatalf("非 TCP 对端应跳过: %v", err)
	}
}

// fakeConn 可注入 RemoteAddr 的最小 net.Conn（建连后核对路径）。
type fakeConn struct {
	addr   net.Addr
	closed bool
}

func (c *fakeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (c *fakeConn) Write([]byte) (int, error)        { return 0, nil }
func (c *fakeConn) Close() error                     { c.closed = true; return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return c.addr }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

// TestMetadataDialGuardRemoteAddr 建连后真实对端不是已校验 IP：断开并报错。
func TestMetadataDialGuardRemoteAddr(t *testing.T) {
	fc := &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP("fd00:ec2::254")}}
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return fc, nil
	}}
	installTrustedDial(tr)
	if _, err := tr.DialContext(context.Background(), "tcp", "10.0.0.1:80"); err == nil {
		t.Fatal("建连后核对到不一致地址应拒绝")
	}
	if !fc.closed {
		t.Fatal("被拒连接必须关闭")
	}
}

func TestCheckTrustedURL(t *testing.T) {
	if err := checkTrustedURL(nil); err == nil {
		t.Fatal("nil URL 应拒绝")
	}
	if err := checkTrustedURL(&url.URL{Scheme: "ftp", Host: "1.1.1.1"}); err == nil {
		t.Fatal("非 http/https 应拒绝")
	}
	if err := checkTrustedURL(&url.URL{Scheme: "http"}); err == nil {
		t.Fatal("缺 host 应拒绝")
	}
	if err := checkTrustedURL(&url.URL{Scheme: "http", Host: "example.com"}); err != nil {
		t.Fatalf("域名应放行（拨号层判）: %v", err)
	}
}

// TestTrustedTransportBoundaries 传输层边界：克隆 *http.Transport 不修改调用方；
// 无 DialContext 时装默认拨号器。
func TestTrustedTransportBoundaries(t *testing.T) {
	resetSettings(t)
	Configure(ModeWarn, nil, logx.Nop())
	baseTr := &http.Transport{}
	base := &http.Client{Transport: baseTr}
	c := TrustedClient(base)
	if c.Transport == baseTr {
		t.Fatal("必须克隆传输层，不得修改调用方")
	}
	if baseTr.DialContext != nil {
		t.Fatal("不得修改调用方 transport 的 DialContext")
	}
	if _, ok := c.Transport.(*http.Transport); !ok {
		t.Fatalf("可信档应返回 *http.Transport，got %T", c.Transport)
	}

	tr := &http.Transport{}
	installTrustedDial(tr)
	if tr.DialContext == nil {
		t.Fatal("无 DialContext 时应装默认拨号器")
	}
}

func TestTrustedDialFailurePaths(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}, nil
	}

	sentinel := errors.New("dial failed")
	_, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(context.Context, string, string) (net.Conn, error) { return nil, sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("全部地址拨号失败应返回底层错误: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	_, err = trustedDial(ctx, "tcp", "gw.internal:80",
		func(context.Context, string, string) (net.Conn, error) {
			cancel()
			return nil, sentinel
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 取消应优先返回: %v", err)
	}
}
