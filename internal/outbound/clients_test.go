package outbound

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

// TestSemiTrustedWarnNoDialGuard warn = log-but-allow：不得在拨号层拦截
// link-local/元数据（netguard 会硬拦，故 warn 不挂它的拨号守卫）。
func TestSemiTrustedWarnNoDialGuard(t *testing.T) {
	resetSettings(t)
	sentinel := errors.New("dial-called")
	base := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, sentinel
		},
	}}

	Configure(ModeWarn, nil, logx.Nop())
	c := SemiTrustedClient(base)
	if _, err := c.Get("http://169.254.169.254/"); !errors.Is(err, sentinel) {
		t.Fatalf("warn 应到达底层拨号（记录但放行），got %v", err)
	}

	Configure(ModeEnforce, nil, logx.Nop())
	c = SemiTrustedClient(base)
	if _, err := c.Get("http://169.254.169.254/"); errors.Is(err, sentinel) {
		t.Fatal("enforce 应在拨号层之前拦截")
	}
}

// TestSemiTrustedWarnMetadataLogs warn 对回环/链路本地/元数据都放行且留痕。
func TestSemiTrustedWarnMetadataLogs(t *testing.T) {
	resetSettings(t)
	lg := newRecordLogger()
	Configure(ModeWarn, nil, lg)
	for _, raw := range []string{
		"http://127.0.0.1/", "http://169.254.169.254/", "http://[fe80::1]/", "http://0.0.0.0/",
	} {
		if err := CheckSemiTrusted(raw); err != nil {
			t.Fatalf("warn 应放行 %s: %v", raw, err)
		}
	}
	if lg.count() < 4 {
		t.Fatalf("warn 应逐条留痕: %v", lg.warns)
	}
}

// TestSemiTrustedEnforceRedirectChain 真实 302 链：公网起点 → 127.0.0.1。
// origin 是真实 httptest server，经自定义 RoundTripper 以公网名字访问
// （enforce + 自定义 Transport 走失败软着陆，CheckRedirect 仍在守）。
func TestSemiTrustedEnforceRedirectChain(t *testing.T) {
	resetSettings(t)
	Configure(ModeEnforce, nil, logx.Nop())
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer origin.Close()
	originAddr := strings.TrimPrefix(origin.URL, "http://")
	base := &http.Client{
		Timeout: 5 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = "http"
			clone.URL.Host = originAddr
			return http.DefaultTransport.RoundTrip(clone)
		}),
	}
	c := SemiTrustedClient(base)
	_, err := c.Get("http://public.example/")
	if err == nil {
		t.Fatal("公网起点 302 到 127.0.0.1 在 enforce 下必须拒绝")
	}
	if !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("拒绝原因应指向重定向目标: %v", err)
	}
}
