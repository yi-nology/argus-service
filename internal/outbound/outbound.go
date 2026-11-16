// Package outbound 出站守卫（#44/#45）：给「服务端代发 HTTP」的配置面出口提供
// 带 SSRF 防护的 HTTP 客户端。防线在**拨号层**——写面校验（URL 构造时早失败）
// 只是便利，出站防线的完整形态是：解析 → 逐 IP 过闸 → 并发拨号 → 建连后核对
// 真实 RemoteAddr。
//
// # 与 guardx/egress、agent-harness/netguard 的分工（为何自建拨号）
//
// IP 放行面**单源 oagentkit/guardx/egress**（回环/私网按 Policy 放行；链路本地
// （云元数据）/CGNAT/保留段一律拒，不受档位影响）——本包不自持网段清单，也不引
// 第二套判定。不直接复用 agent-harness/netguard：其名字层（解析前按名拒）无条件
// 拒 .internal/.local/.lan 等后缀，而 argus 的配置面目标（review-service 等集群内
// 服务）合法落在这些命名空间，名字层会误伤部署——这正是 #45 记录的「自建拨号」
// 缘由。故本包**不做名字判定**：域名解析后逐 IP 过 egress 策略，解析变体（exotic
// IPv4 等）自然被「按解析结果判 IP」兜住。
//
// # 可信档（TrustedClient）
//
// sink URL 来自运维配置（ARGUS_SINK_URL），非用户可控输入：能否抵达内网/回环
// 本身就是该出口的既有能力，放行面取 egress.SelfService（私网+回环放行）；
// 恒拒段（元数据/保留段）不受档位影响，仍拦。未来不可信输入面（代发类出口）
// 用 Client + 零值 Policy（只放公网）。
//
// # warn/enforce 两态（#44）
//
//   - enforce（默认，零值安全）：违规即拒。先校验**全部**解析 IP，任一不过闸则
//     一个都不拨（与 agent-harness/netguard 同口径：逐个判而非只看第一个——DNS
//     可给多条记录，挑到哪个是实现的事）。
//   - warn（影子灰度）：违规逐条经 OnViolation 留痕后按未守卫行为放行——用于
//     切 enforce 前实测「守卫若生效会拦住什么」。解析失败不是违规（是错误），
//     两态都 fail-closed。
//
// # Happy Eyeballs（#45）
//
// 域名解析出多个候选 IP 时并发拨号（RFC 8305 形态：首号即拨，其余每隔
// FallbackDelay 启动），首个建连成功者胜出、其余取消——双栈域名首地址黑洞不再
// 吃满请求 deadline。胜者还要过 RemoteAddr 复核：预检到建连之间仍有窗口，实际
// 连上的对端地址是唯一事实，复核不过立即断开（enforce）或留痕放行（warn）。
package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/oagentkit/guardx/egress"
)

// Mode 出站守卫两态：enforce = 违规拒发（零值安全默认）；warn = 影子留痕放行。
type Mode string

const (
	// ModeEnforce 违规拒绝（默认）。零值 Mode（""）经 ModeOf 也归到这一档。
	ModeEnforce Mode = "enforce"
	// ModeWarn 影子灰度：违规留痕（OnViolation）后放行。
	ModeWarn Mode = "warn"
)

// ModeOf 归一两态词表：仅 "warn"（大小写不敏感、去空白）为影子档，其余
// （含零值/配置缺省/任意未知名）一律 enforce——与 argus 预算强制
// （ARGUS_BUDGET_ENFORCEMENT 仅 "hard" 为硬）同方向的保守归一。
func ModeOf(v string) Mode {
	if strings.EqualFold(strings.TrimSpace(v), string(ModeWarn)) {
		return ModeWarn
	}
	return ModeEnforce
}

// ErrBlocked 出站被守卫拒绝的哨兵：调用方 errors.Is 统一判定，不必解构具体类型。
var ErrBlocked = errors.New("outbound: 出站目标被守卫拒绝")

// BlockedError 单次拒绝结论：带 host/ip 与命中原因，Unwrap 出 ErrBlocked。
type BlockedError struct {
	Host   string
	IP     string
	Reason string
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("outbound: 出站目标被守卫拒绝（host=%s ip=%s 原因=%s）", e.Host, e.IP, e.Reason)
}

func (e *BlockedError) Unwrap() error { return ErrBlocked }

// Options 守卫选项。零值 = enforce + 可信档放行面（经 TrustedClient）+ 缺省解析器
// 与拨号器 + 300ms FallbackDelay——即「接上就有防线」的缺省形态。
type Options struct {
	// Mode 两态；零值 = enforce（ModeOf 归一）。
	Mode Mode
	// Allow IP 放行面（单源 guardx/egress）。TrustedClient 会把零值补成
	// egress.SelfService（可信档）；经 Client 直构造时零值 = 只放公网。
	Allow egress.Policy
	// Lookup 主机解析器。nil = net.DefaultResolver.LookupIP(ctx, "ip", host)。
	// 测试注入假解析器，即可把「预检拦下 / RemoteAddr 复核拦下 / 首地址黑洞」
	// 逐条钉成确定性用例（真实 DNS 不可控，判据必须可测）。
	Lookup func(ctx context.Context, host string) ([]net.IP, error)
	// Dial 底层拨号函数（net.Dialer.DialContext 同形）。nil = 带 10s 超时的
	// net.Dialer（无上限的一次卡住拨号会把调用方一起挂住）。测试注入假拨号器，
	// 即可确定性模拟黑洞/拒连/伪造对端地址——判据可测才可验。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// FallbackDelay Happy Eyeballs 次选启动间隔（RFC 8305 形态：第 i 个候选在
	// i*间隔时启动）。零值 = 300ms 缺省；负值 = 不 stagger、全部立即并发。
	FallbackDelay time.Duration
	// OnViolation 影子档留痕钩子（仅 warn 档触发；enforce 档拒绝经错误返回，
	// 不重复回调）。nil = 静默。event 为一条人读摘要（host/ip/原因）。
	OnViolation func(event string)
}

// 缺省口径：拨号超时与 RFC 8305 缺省 FallbackDelay。
const (
	dialTimeout          = 10 * time.Second
	defaultFallbackDelay = 300 * time.Millisecond
)

// TrustedClient 可信档出站客户端：拨号层守卫 + Happy Eyeballs 并发拨号。
//
// base 为 nil 时按 http.DefaultClient 形态取；base 的 Timeout/Jar/CheckRedirect
// 原样保留（CheckRedirect 会被守卫的逐跳判定接手——见下）。base.Transport 为 nil
// 或 *http.Transport 时克隆并挂守卫（不修改 base，也不碰全局默认传输器）；是其他
// 自定义 RoundTripper 时返回错误而不是悄悄放行——「守卫看起来接上了，实际没接」
// 的失败形态必须在这里炸出来。
//
// 可信档放行面：opts.Allow 零值时补 egress.SelfService（私网+回环放行，配置面
// 目标的既有能力域）；显式传不可信面请用 Client。
func TrustedClient(base *http.Client, opts Options) (*http.Client, error) {
	if opts.Allow == (egress.Policy{}) {
		opts.Allow = egress.SelfService // 可信档：私网+回环放行（恒拒段仍拦）
	}
	return Client(base, opts)
}

// Client 通用出站客户端：放行面取 opts.Allow（零值 = egress.PublicOnly，只放
// 公网——面向不可信输入的出口）。其余语义同 TrustedClient。
func Client(base *http.Client, opts Options) (*http.Client, error) {
	g := newGuard(opts)
	c := &http.Client{}
	if base != nil {
		// http.Client 无锁，浅拷贝安全；Transport 随后被替换成克隆，base 不受影响。
		cc := *base
		c = &cc
	}
	switch tr := c.Transport.(type) {
	case nil:
		c.Transport = g.transport(nil)
	case *http.Transport:
		c.Transport = g.transport(tr)
	default:
		return nil, fmt.Errorf("outbound: 客户端 Transport 是自定义 RoundTripper（%T），"+
			"无法挂拨号层守卫：拨号发生在传输层内部；请改用 *http.Transport", tr)
	}
	// 纵深：逐跳判定（egress.CheckRedirect 自带 10 跳上限）。写面只判首跳 URL，
	// 拨号层守卫兜住重定向目标；代理场景下拨号地址是代理地址，由这一层补位。
	c.CheckRedirect = g.allow.CheckRedirect
	return c, nil
}

// guard 守卫本体：Options 的不可变快照（构造期归一，运行期零分支漂移）。
type guard struct {
	mode          Mode
	allow         egress.Policy
	lookup        func(ctx context.Context, host string) ([]net.IP, error)
	dialFn        func(ctx context.Context, network, addr string) (net.Conn, error)
	fallbackDelay time.Duration
	onViolation   func(event string)
}

func newGuard(opts Options) *guard {
	g := &guard{mode: ModeOf(string(opts.Mode)), allow: opts.Allow, onViolation: opts.OnViolation}
	g.lookup = opts.Lookup
	if g.lookup == nil {
		g.lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	g.dialFn = opts.Dial
	if g.dialFn == nil {
		var d net.Dialer
		d.Timeout = dialTimeout
		g.dialFn = d.DialContext
	}
	g.fallbackDelay = opts.FallbackDelay
	if g.fallbackDelay == 0 {
		g.fallbackDelay = defaultFallbackDelay
	}
	return g
}

// transport 克隆 base（nil = http.DefaultTransport）并挂上守卫拨号器。
func (g *guard) transport(base *http.Transport) *http.Transport {
	var t *http.Transport
	if base == nil {
		t = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		t = base.Clone()
	}
	t.DialContext = g.dial
	return t
}

// dial 守卫拨号器（挂 Transport.DialContext）：IP 字面量直判直拨；域名先解析、
// enforce 档全量过闸（任一违规一个不拨），再并发拨号取首个成功连接。
func (g *guard) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("outbound: 拨号地址非法 %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip != nil {
		// 字面量：无解析面（无 rebinding 窗口），单候选直判直拨。
		if err := g.checkIP(host, ip, "预检"); err != nil {
			if g.mode != ModeWarn {
				return nil, err
			}
			g.violate(err.Error())
		}
		return g.dialOne(ctx, network, ip, port, true)
	}
	ips, err := g.lookup(ctx, host)
	if err != nil {
		// 解析失败 fail-closed（两态一致）：DNS 黑洞/超时不是「违规」，
		// 是错误——不该被影子档放行。
		return nil, fmt.Errorf("outbound: 出站目标解析失败 host=%q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("outbound: 出站目标解析为空 host=%q: %w", host, ErrBlocked)
	}
	// 先校验**全部** IP（两态都全量过闸，且都发生在拨号之前）：enforce 任一
	// 违规即整体拒、一个不拨；warn 逐条留痕后照拨。只看第一个等于赌拨号器
	// 挑对——DNS 可以给多条记录，其中一条是内网/元数据地址。留痕/拒绝都必须
	// 确定性发生：并发拨号胜者会取消其余在途，把判定挪到 dialOne 里就会
	// 与「首个成功连接」竞速（违规可能永远轮不到留痕）。
	for _, ip := range ips {
		if err := g.checkIP(host, ip, "预检"); err != nil {
			if g.mode != ModeWarn {
				return nil, err
			}
			g.violate(err.Error())
		}
	}
	return g.dialRace(ctx, network, port, ips)
}

// dialRace Happy Eyeballs 并发拨号：首号即拨，其余每隔 FallbackDelay 启动
// （FallbackDelay < 0 时全部立即启动），首个建连成功者胜出，其余随 ctx 取消。
// 单候选走直拨，不起并发面。
func (g *guard) dialRace(ctx context.Context, network, port string, ips []net.IP) (net.Conn, error) {
	if len(ips) == 1 {
		return g.dialOne(ctx, network, ips[0], port, false)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type attempt struct {
		idx  int
		conn net.Conn
		err  error
	}
	ch := make(chan attempt, len(ips))
	start := func(i int) {
		go func() {
			c, err := g.dialOne(ctx, network, ips[i], port, false)
			ch <- attempt{idx: i, conn: c, err: err}
		}()
	}

	start(0)
	outstanding := 1
	launched := 1
	if g.fallbackDelay < 0 {
		for i := 1; i < len(ips); i++ {
			start(i)
		}
		launched, outstanding = len(ips), len(ips)
	}

	fail := make([]error, len(ips))
	var timer *time.Timer
	var timerC <-chan time.Time
	armNext := func() {
		if launched >= len(ips) {
			return
		}
		timer = time.NewTimer(time.Duration(launched) * g.fallbackDelay)
		timerC = timer.C
	}
	if g.fallbackDelay >= 0 {
		armNext()
	}

	var winner net.Conn
	stop := false // 外层取消后停启动（在途的仍等收敛）
	for winner == nil && !stop && (outstanding > 0 || launched < len(ips)) {
		select {
		case at := <-ch:
			outstanding--
			if at.err != nil {
				fail[at.idx] = at.err
				continue
			}
			winner = at.conn
		case <-timerC:
			start(launched)
			launched++
			outstanding++
			armNext()
		case <-ctx.Done():
			// 外层取消/超时：停启动，已启动的随 ctx 收敛后经 ch 归还。
			stop = true
			timerC = nil
		}
	}
	if timer != nil {
		timer.Stop()
	}
	if winner != nil {
		return winner, nil // defer cancel 收掉其余在途拨号
	}
	// 全败：取最低下标的错误（顺序拨号会先遇到的那个，报错口径可复现）。
	for _, err := range fail {
		if err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("outbound: 并发拨号全部失败（无候选返回结果）")
}

// dialOne 单候选拨号 + 端点复核。prechecked = 预检已在 dial 层做过（并发拨号
// 路径——预检在启动并发面前全量完成，此处不重判）；false 时就地补判（IP
// 字面量直拨路径）。enforce 档违规即拒；warn 档留痕后放行（影子语义）。
func (g *guard) dialOne(ctx context.Context, network string, ip net.IP, port string, prechecked bool) (net.Conn, error) {
	host := ip.String()
	if !prechecked {
		if err := g.checkIP(host, ip, "预检"); err != nil {
			if g.mode != ModeWarn {
				return nil, err
			}
			g.violate(err.Error())
		}
	}
	conn, err := g.dialFn(ctx, network, net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	if err := checkRemoteAddr(conn, g.allow); err != nil {
		if g.mode != ModeWarn {
			_ = conn.Close()
			return nil, err
		}
		g.violate(err.Error())
	}
	return conn, nil
}

// checkIP 单个 IP 过 egress 放行面（唯一判据单源；恒拒段不受放行旗标影响）。
// 拒绝结论统一 BlockedError（Unwrap 出 ErrBlocked 哨兵），调用方 errors.Is 判定。
func (g *guard) checkIP(host string, ip net.IP, stage string) error {
	if err := egress.CheckHostWith(ip.String(), g.allow); err != nil {
		return &BlockedError{Host: host, IP: ip.String(), Reason: fmt.Sprintf("%s: %v", stage, err)}
	}
	return nil
}

// checkRemoteAddr 端点复核：建连后核对真实对端地址仍是放行面内的 IP。
// 闭合「预检 → 建连」之间的窗口——DNS 在那之后改口，实际连上的就不是预检过的
// 地址；对端地址是唯一事实。
func checkRemoteAddr(conn net.Conn, allow egress.Policy) error {
	if conn == nil || conn.RemoteAddr() == nil {
		return fmt.Errorf("outbound: 拨号结果无对端地址，无法复核: %w", ErrBlocked)
	}
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		// 对端地址形态异常：不猜，按拒处理。
		return fmt.Errorf("outbound: 对端地址 %q 解析失败: %w: %w", conn.RemoteAddr().String(), err, ErrBlocked)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("outbound: 对端地址 %q 不是 IP 字面量，无法复核: %w", host, ErrBlocked)
	}
	if err := egress.CheckHostWith(ip.String(), allow); err != nil {
		return &BlockedError{Host: host, IP: ip.String(), Reason: fmt.Sprintf("RemoteAddr 复核: %v", err)}
	}
	return nil
}

// violate 影子档留痕（OnViolation nil = 静默）。
func (g *guard) violate(event string) {
	if g.onViolation != nil {
		g.onViolation(event)
	}
}
