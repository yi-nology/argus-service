package outbound

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oagentkit/guardx/egress"
)

// ── 测试基建：可控拨号器 + 可塑对端地址的连接 ──
//
// 守卫判据必须确定性可测：解析器、拨号行为、对端地址三处全部可注入，
// 才能把「预检拦下 / RemoteAddr 复核拦下 / 首地址黑洞并发救回」逐条钉死。

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

// plasticConn 委托真 conn 但对端地址可伪造——RemoteAddr 复核用例的支点。
type plasticConn struct {
	net.Conn
	remote net.Addr
	closed atomic.Bool
}

func (c *plasticConn) RemoteAddr() net.Addr { return c.remote }
func (c *plasticConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// fakeDialer 行为表驱动：按「ip:port」给拨号器编程。
//   - hang: 阻塞到 ctx 取消（黑洞地址）
//   - fail: 立即报错
//   - delay: 拨号耗时
//   - remote: 伪造对端地址（缺省 = 拨号地址本身）
type fakeDialer struct {
	mu      sync.Mutex
	started []string
	hang    map[string]bool
	fail    map[string]error
	delay   map[string]time.Duration
	remote  map[string]string
	dials   atomic.Int32
}

func (d *fakeDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.dials.Add(1)
	d.mu.Lock()
	d.started = append(d.started, addr)
	hang, fail, delay, remote := d.hang[addr], d.fail[addr], d.delay[addr], d.remote[addr]
	d.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if fail != nil {
		return nil, fail
	}
	c, peer := net.Pipe()
	_ = peer
	r := addr
	if remote != "" {
		r = remote
	}
	return &plasticConn{Conn: c, remote: fakeAddr(r)}, nil
}

func (d *fakeDialer) startedList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.started...)
}

func fixedLookup(ips ...string) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

// dialTransport 直拨传输层守卫（守卫判据的单测缝隙，无需完整 HTTP 往返）。
func dialTransport(t *testing.T, cl *http.Client, addr string) (net.Conn, error) {
	return dialTransportCtx(t, cl, addr, 5*time.Second)
}

func dialTransportCtx(t *testing.T, cl *http.Client, addr string, budget time.Duration) (net.Conn, error) {
	t.Helper()
	tr, ok := cl.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 应为 *http.Transport，got %T", cl.Transport)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	return tr.DialContext(ctx, "tcp", addr)
}

// guardWithLookup 组装「注入解析器 + 注入拨号器」的被测客户端。
func guardWithLookup(t *testing.T, opts Options, ips []string, d *fakeDialer) *http.Client {
	t.Helper()
	opts.Lookup = fixedLookup(ips...)
	opts.Dial = d.DialContext
	opts.FallbackDelay = 10 * time.Millisecond
	cl, err := Client(&http.Client{}, opts)
	if err != nil {
		t.Fatalf("构造客户端: %v", err)
	}
	return cl
}

// ── #44：守卫判据与两态 ──

func TestEnforceBlocksMixedCandidatesBeforeAnyDial(t *testing.T) {
	d := &fakeDialer{}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}}, // 只放公网
		[]string{"1.1.1.1", "10.0.0.7"}, d) // 混入私网
	_, err := dialTransport(t, cl, "mixed.example:8080")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("候选混入私网应整体拒（ErrBlocked），got %v", err)
	}
	if n := d.dials.Load(); n != 0 {
		t.Fatalf("先校验后拨：任一违规应一个都不拨，got %d 次拨号", n)
	}
}

// warn 影子档：同样的违规候选在拨号前逐条留痕（确定性——不与胜者竞速），
// 然后按未守卫行为放行（连接照建；胜者取消其余在途候选属拨号经济学，
// 不属影子契约）。
func TestWarnShadowLogsAndProceeds(t *testing.T) {
	d := &fakeDialer{}
	var shadow atomic.Int32
	cl := guardWithLookup(t, Options{
		Allow: egress.Policy{},
		Mode:  ModeWarn,
		OnViolation: func(string) {
			shadow.Add(1)
		},
	}, []string{"1.1.1.1", "10.0.0.7"}, d)
	conn, err := dialTransport(t, cl, "mixed.example:8080")
	if err != nil {
		t.Fatalf("影子档应放行: %v", err)
	}
	defer conn.Close()
	if n := shadow.Load(); n != 1 {
		t.Fatalf("预检应逐条留痕（恰 1 个违规候选），got %d", n)
	}
	if n := d.dials.Load(); n < 1 {
		t.Fatalf("影子档应照常拨号，got %d", n)
	}
}

// RemoteAddr 复核（enforce）：预检过的地址建连后对端却是元数据段 → 拒并断开。
func TestEnforceRemoteAddrRecheckClosesConn(t *testing.T) {
	d := &fakeDialer{remote: map[string]string{"1.1.1.1:80": "169.254.169.254:9"}}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}},
		[]string{"1.1.1.1"}, d)
	conn, err := dialTransport(t, cl, "rebind.example:80")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("对端落在恒拒段应拒: %v", err)
	}
	if conn != nil {
		t.Fatal("复核拒绝应返回 nil conn")
	}
	if n := d.dials.Load(); n != 1 {
		t.Fatalf("复核发生在建连之后（先拨后核），got %d 次拨号", n)
	}
}

// warn 档 RemoteAddr 复核违规：留痕放行（影子）。
func TestWarnRemoteAddrViolationShadowProceeds(t *testing.T) {
	d := &fakeDialer{remote: map[string]string{"1.1.1.1:80": "169.254.169.254:9"}}
	var shadow atomic.Int32
	cl := guardWithLookup(t, Options{
		Allow:       egress.Policy{},
		Mode:        ModeWarn,
		OnViolation: func(string) { shadow.Add(1) },
	}, []string{"1.1.1.1"}, d)
	conn, err := dialTransport(t, cl, "rebind.example:80")
	if err != nil {
		t.Fatalf("影子档应放行: %v", err)
	}
	defer conn.Close()
	if shadow.Load() == 0 {
		t.Fatal("RemoteAddr 违规应留痕")
	}
}

// 恒拒段不受可信档放行面影响：元数据字面量在预检即拦，零拨号。
func TestTrustedTierStillBlocksMetadataLiteral(t *testing.T) {
	d := &fakeDialer{}
	cl, err := TrustedClient(&http.Client{}, Options{Dial: d.DialContext})
	if err != nil {
		t.Fatalf("构造可信档客户端: %v", err)
	}
	_, err = dialTransport(t, cl, "169.254.169.254:80")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("元数据段应恒拒: %v", err)
	}
	if n := d.dials.Load(); n != 0 {
		t.Fatalf("字面量预检即拦应零拨号，got %d", n)
	}
}

// 解析失败/空结果两态都 fail-closed（不是「违规」，不进影子放行）。
func TestLookupFailureFailsClosedBothModes(t *testing.T) {
	for _, mode := range []Mode{ModeEnforce, ModeWarn} {
		d := &fakeDialer{}
		cl, err := TrustedClient(&http.Client{}, Options{
			Mode: mode,
			Dial: d.DialContext,
			Lookup: func(context.Context, string) ([]net.IP, error) {
				return nil, errors.New("dns 黑洞")
			},
		})
		if err != nil {
			t.Fatalf("构造: %v", err)
		}
		if _, err := dialTransport(t, cl, "blackhole.example:80"); err == nil {
			t.Fatalf("%s: 解析失败应 fail-closed", mode)
		}
		if n := d.dials.Load(); n != 0 {
			t.Fatalf("%s: 解析失败不应拨号，got %d", mode, n)
		}
	}
}

func TestLookupEmptyFailsClosed(t *testing.T) {
	d := &fakeDialer{}
	cl := guardWithLookup(t, Options{Allow: egress.SelfService},
		nil, d)
	if _, err := dialTransport(t, cl, "empty.example:80"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("解析为空应拒: %v", err)
	}
	if n := d.dials.Load(); n != 0 {
		t.Fatalf("空解析不应拨号，got %d", n)
	}
}

// exotic IPv4 词法变体走解析路径：按解析结果判 IP，仍 fail-closed。
func TestExoticIPv4LexicalVariantFailClosed(t *testing.T) {
	d := &fakeDialer{}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}},
		[]string{"127.0.0.1"}, d) // cgo 解析器把 0177.0.0.1 解成回环
	_, err := dialTransport(t, cl, "0177.0.0.1:80")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("exotic 变体解析出回环应拒: %v", err)
	}
	if n := d.dials.Load(); n != 0 {
		t.Fatalf("应零拨号，got %d", n)
	}
}

// ModeOf 保守归一：仅 warn 入影子档，未知/零值一律 enforce（零值安全默认）。
func TestModeOf(t *testing.T) {
	cases := map[string]Mode{
		"":        ModeEnforce,
		"enforce": ModeEnforce,
		"warn":    ModeWarn,
		" WARN ":  ModeWarn,
		"soft":    ModeEnforce,
		"block":   ModeEnforce,
	}
	for in, want := range cases {
		if got := ModeOf(in); got != want {
			t.Fatalf("ModeOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// 自定义 RoundTripper 挂不上拨号守卫：显式报错而不是悄悄放行。
func TestClientRejectsCustomRoundTripper(t *testing.T) {
	rt := &fakeRT{}
	if _, err := Client(&http.Client{Transport: rt}, Options{}); err == nil {
		t.Fatal("自定义 RoundTripper 应报错")
	}
}

type fakeRT struct{ http.RoundTripper }

// ── #45：Happy Eyeballs ──

// 核心回归：首地址黑洞、次地址秒连 —— 并发拨号把请求从「吃满 deadline」里救回。
// 顺序拨号会卡死在黑洞候选上（hang 直到取消），并发档应在时限内拿次候选连接。
func TestHappyEyeballsBlackholeFirstCandidate(t *testing.T) {
	d := &fakeDialer{hang: map[string]bool{"1.1.1.1:80": true}}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}}, // 只放公网
		[]string{"1.1.1.1", "1.1.1.2"}, d)

	started := time.Now()
	conn, err := dialTransport(t, cl, "dualstack.example:80")
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("并发拨号应取次候选成功: %v", err)
	}
	defer conn.Close()

	if elapsed > 2*time.Second {
		t.Fatalf("黑洞首址不应吃满时限，耗时 %v", elapsed)
	}
	if got := conn.RemoteAddr().String(); got != "1.1.1.2:80" {
		t.Fatalf("胜者应是次候选连接，got %s", got)
	}
	starts := d.startedList()
	if len(starts) != 2 || starts[0] != "1.1.1.1:80" || starts[1] != "1.1.1.2:80" {
		t.Fatalf("两候选都应被启动（并发证据），started=%v", starts)
	}
}

// 全部候选都黑洞：错误可返回（不挂死），随调用方 ctx 及时收敛。
func TestAllCandidatesBlackholeReturnsError(t *testing.T) {
	d := &fakeDialer{hang: map[string]bool{"1.1.1.1:80": true, "1.1.1.2:80": true}}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}},
		[]string{"1.1.1.1", "1.1.1.2"}, d)
	started := time.Now()
	_, err := dialTransportCtx(t, cl, "alldown.example:80", 800*time.Millisecond)
	if err == nil {
		t.Fatal("全黑洞应报错")
	}
	if elapsed := time.Since(started); elapsed > 1500*time.Millisecond {
		t.Fatalf("全败应随 ctx 及时收敛，耗时 %v", elapsed)
	}
}

// 首地址失败（非黑洞，立即报错）：次地址照常接管。
func TestFirstCandidateFailsSecondWins(t *testing.T) {
	d := &fakeDialer{fail: map[string]error{"1.1.1.1:80": errors.New("refused")}}
	cl := guardWithLookup(t, Options{Allow: egress.Policy{}},
		[]string{"1.1.1.1", "1.1.1.2"}, d)
	conn, err := dialTransport(t, cl, "refused.example:80")
	if err != nil {
		t.Fatalf("次地址应接管: %v", err)
	}
	conn.Close()
	if got := conn.RemoteAddr().String(); got != "1.1.1.2:80" {
		t.Fatalf("胜者应是次候选，got %s", got)
	}
}

// 单候选直拨，不起并发面（行为与拨号器直连等价 + 守卫复核）。
func TestSingleCandidateDirectDial(t *testing.T) {
	d := &fakeDialer{}
	cl := guardWithLookup(t, Options{Allow: egress.SelfService},
		[]string{"10.0.0.9"}, d) // 可信档私网放行
	conn, err := dialTransport(t, cl, "internal-svc.example:8080")
	if err != nil {
		t.Fatalf("可信档应放行私网: %v", err)
	}
	conn.Close()
	if n := d.dials.Load(); n != 1 {
		t.Fatalf("单候选一次拨号，got %d", n)
	}
}

// IP 字面量主机：无解析面，直判直拨（可信档放行私网字面量）。
func TestIPLiteralTrustedTierDirect(t *testing.T) {
	d := &fakeDialer{}
	cl, err := TrustedClient(&http.Client{}, Options{Dial: d.DialContext})
	if err != nil {
		t.Fatalf("构造: %v", err)
	}
	conn, err := dialTransport(t, cl, "10.1.2.3:9000")
	if err != nil {
		t.Fatalf("可信档字面量私网应放行: %v", err)
	}
	conn.Close()
	if n := d.dials.Load(); n != 1 {
		t.Fatalf("字面量一次拨号，got %d", n)
	}
}

// 端到端（真回环）：可信档客户端经 httptest 真实连通（守卫不误伤回环自建服务）。
func TestTrustedClientLoopbackEndToEnd(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)

	cl, err := TrustedClient(&http.Client{Timeout: 3 * time.Second}, Options{})
	if err != nil {
		t.Fatalf("构造: %v", err)
	}
	resp, err := cl.Get(srv.URL + "/ping")
	if err != nil {
		t.Fatalf("可信档应放行回环自建服务: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || gotPath != "/ping" {
		t.Fatalf("status=%d path=%q", resp.StatusCode, gotPath)
	}
}
