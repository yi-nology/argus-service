package outbound

import (
	"context"
	"errors"
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

// TestSemiTrustedClientModes enforce 拒绝回环、warn/off 放行（生产行为不变）。
func TestSemiTrustedClientModes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cases := []struct {
		name    string
		mode    Mode
		wantErr bool
	}{
		{"enforce", ModeEnforce, true},
		{"warn", ModeWarn, false},
		{"off", ModeOff, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetSettings(t)
			Configure(tc.mode, nil, logx.Nop())
			c := SemiTrustedClient(&http.Client{Timeout: 2 * time.Second})
			resp, err := c.Get(srv.URL)
			if tc.wantErr {
				if err == nil {
					_ = resp.Body.Close()
					t.Fatal("enforce 应拒绝回环目标")
				}
				return
			}
			if err != nil {
				t.Fatalf("%s 模式应放行: %v", tc.name, err)
			}
			_ = resp.Body.Close()
		})
	}
}

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

// TestSemiTrustedRedirectRevalidates 每一跳都复验：enforce 拒绝、warn 记录后放行。
func TestSemiTrustedRedirectRevalidates(t *testing.T) {
	resetSettings(t)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)}
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)

	Configure(ModeEnforce, nil, logx.Nop())
	if err := SemiTrustedClient(&http.Client{}).CheckRedirect(req, via); err == nil {
		t.Fatal("enforce 下重定向到回环应拒绝")
	}

	lg := newRecordLogger()
	Configure(ModeWarn, nil, lg)
	if err := SemiTrustedClient(&http.Client{}).CheckRedirect(req, via); err != nil {
		t.Fatalf("warn 下重定向应放行: %v", err)
	}
	if lg.first() != "argus.outbound.denied" {
		t.Fatalf("warn 应记录逐跳违规: %v", lg.warns)
	}
}

func TestRedirectHopLimit(t *testing.T) {
	resetSettings(t)
	Configure(ModeWarn, nil, logx.Nop())
	c := SemiTrustedClient(&http.Client{})
	req := httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)
	via := make([]*http.Request, maxRedirects)
	if err := c.CheckRedirect(req, via); err == nil {
		t.Fatal("超过跳数上限应拒绝")
	}
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
}

func TestCheckTrustedDialAddr(t *testing.T) {
	deny := []string{
		"169.254.169.254:80", "[fd00:ec2::254]:443", "[::ffff:169.254.169.254]:80",
		"100.100.100.200:80", "[fe80::1]:80", "0.0.0.0:80", "metadata.google.internal:80",
	}
	for _, addr := range deny {
		if err := checkTrustedDialAddr(context.Background(), addr); err == nil {
			t.Fatalf("%s 应拒绝", addr)
		}
	}
	allow := []string{"127.0.0.1:80", "10.0.0.1:80", "1.1.1.1:443", "not-a-dial-addr"}
	for _, addr := range allow {
		if err := checkTrustedDialAddr(context.Background(), addr); err != nil {
			t.Fatalf("%s 应放行/交给 netguard: %v", addr, err)
		}
	}
}

// TestCheckTrustedDialAddrResolvedMetadata 域名解析到元数据 ULA
// （fd00:ec2::254）时必须在建连前拒绝——egress 把 ULA 当私网放行。
func TestCheckTrustedDialAddrResolvedMetadata(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })

	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("fd00:ec2::254")}, nil
	}
	if err := checkTrustedDialAddr(context.Background(), "evil.example:443"); err == nil {
		t.Fatal("域名解析到元数据 ULA 应拒绝")
	}

	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	if err := checkTrustedDialAddr(context.Background(), "example.com:443"); err != nil {
		t.Fatalf("公网解析结果应放行: %v", err)
	}

	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("dns failure")
	}
	if err := checkTrustedDialAddr(context.Background(), "broken.example:443"); err != nil {
		t.Fatalf("解析失败应交给 netguard: %v", err)
	}
}

// remoteConn 只覆写 RemoteAddr（建连后元数据核对用）。
type remoteConn struct {
	net.Conn
	addr net.Addr
}

func (c remoteConn) RemoteAddr() net.Addr { return c.addr }

func TestCheckTrustedRemoteAddr(t *testing.T) {
	meta := remoteConn{addr: &net.TCPAddr{IP: net.ParseIP("fd00:ec2::254")}}
	if err := checkTrustedRemoteAddr(meta); err == nil {
		t.Fatal("实际对端是元数据 ULA 应拒绝")
	}
	private := remoteConn{addr: &net.TCPAddr{IP: net.ParseIP("10.0.0.1")}}
	if err := checkTrustedRemoteAddr(private); err != nil {
		t.Fatalf("私网对端应放行: %v", err)
	}
	if err := checkTrustedRemoteAddr(nil); err != nil {
		t.Fatalf("nil conn 应安全: %v", err)
	}
	if err := checkTrustedRemoteAddr(remoteConn{addr: &net.UnixAddr{Name: "x", Net: "unix"}}); err != nil {
		t.Fatalf("非 TCP 对端应跳过: %v", err)
	}
}

// roundTripFunc 自定义 RoundTripper（netguard 无法挂拨号守卫的失败软着陆路径）。
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientFailSoftCustomTransport(t *testing.T) {
	resetSettings(t)
	lg := newRecordLogger()
	Configure(ModeEnforce, nil, lg)
	base := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     make(http.Header),
		}, nil
	})}

	c := SemiTrustedClient(base)
	if c == nil || c.CheckRedirect == nil {
		t.Fatal("失败软着陆必须返回可用 client（含 CheckRedirect）")
	}
	if lg.first() != "argus.outbound.guard_unavailable" {
		t.Fatalf("应记录守卫不可用: %v", lg.warns)
	}
	resp, err := c.Get("http://127.0.0.1/")
	if err != nil {
		t.Fatalf("自定义 Transport 不走拨号层，应可请求: %v", err)
	}
	_ = resp.Body.Close()
	// 写面逐跳校验仍然有效。
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)}
	if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil), via); err == nil {
		t.Fatal("失败软着陆仍应保留 URL 级逐跳校验")
	}
	if base.CheckRedirect != nil {
		t.Fatal("不得修改调用方 client")
	}

	tc := TrustedClient(base)
	if tc == nil || tc.CheckRedirect == nil {
		t.Fatal("可信档失败软着陆必须返回可用 client")
	}
}

func TestClientPreservesBaseRedirect(t *testing.T) {
	resetSettings(t)
	called := false
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return nil
	}}
	Configure(ModeEnforce, nil, logx.Nop())
	c := TrustedClient(base)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)}
	if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil), via); err != nil {
		t.Fatalf("可信档应放行回环: %v", err)
	}
	if !called {
		t.Fatal("调用方原有 CheckRedirect 应被链式保留")
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

// TestMetadataDialGuardRemoteAddr 建连后真实对端是元数据 ULA：断开并报错。
func TestMetadataDialGuardRemoteAddr(t *testing.T) {
	fc := &fakeConn{addr: &net.TCPAddr{IP: net.ParseIP("fd00:ec2::254")}}
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return fc, nil
	}}
	c := &http.Client{Transport: tr}
	addMetadataDialGuard(c)
	if _, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("建连后核对到元数据地址应拒绝")
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

func TestNilBaseClients(t *testing.T) {
	resetSettings(t)
	Configure(ModeEnforce, nil, logx.Nop())
	if c := TrustedClient(nil); c == nil || c.CheckRedirect == nil {
		t.Fatal("可信档 nil base 应可用")
	}
	if c := SemiTrustedClient(nil); c == nil || c.CheckRedirect == nil {
		t.Fatal("半可信 nil base 应可用")
	}
	Configure(ModeOff, nil, logx.Nop())
	if c := SemiTrustedClient(nil); c == nil {
		t.Fatal("off + nil base 应可用")
	}
}

func TestSemiTrustedRedirectChainsBase(t *testing.T) {
	resetSettings(t)
	called := false
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return nil
	}}
	Configure(ModeEnforce, nil, logx.Nop())
	c := SemiTrustedClient(base)
	via := []*http.Request{httptest.NewRequest(http.MethodGet, "https://1.1.1.1/", nil)}
	if err := c.CheckRedirect(httptest.NewRequest(http.MethodGet, "https://8.8.8.8/", nil), via); err != nil {
		t.Fatalf("公网跳转应放行: %v", err)
	}
	if !called {
		t.Fatal("半可信档也应链式保留调用方 CheckRedirect")
	}
}
