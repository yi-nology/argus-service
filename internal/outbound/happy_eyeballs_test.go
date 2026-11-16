package outbound

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTrustedDialHappyEyeballs 两个解析结果并发竞速：一个失败一个成功 →
// 成功连接胜出且保持打开。
func TestTrustedDialHappyEyeballs(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	win := newCountingConn("10.0.0.2")
	got, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr == "10.0.0.1:80" {
				return nil, errors.New("first address unreachable")
			}
			return win, nil
		})
	if err != nil {
		t.Fatalf("一个地址失败时另一地址应胜出: %v", err)
	}
	if got != win {
		t.Fatalf("应返回成功地址的连接，got %v", got)
	}
	if win.isClosed() {
		t.Fatal("胜出连接不得被收尾逻辑关闭")
	}
}

// TestTrustedDialHappyEyeballsAllFail 全部地址失败 → 返回错误（错误链保留
// 底层拨号错误）。
func TestTrustedDialHappyEyeballsAllFail(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	errA, errB := errors.New("dial a failed"), errors.New("dial b failed")
	_, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				return nil, errA
			}
			return nil, errB
		})
	if err == nil {
		t.Fatal("全部地址失败应报错")
	}
	if !errors.Is(err, errA) && !errors.Is(err, errB) {
		t.Fatalf("应返回最后收到的底层拨号错误，实得 %v", err)
	}
}

// TestTrustedDialHappyEyeballsClosesLateSuccess 胜出后晚到的成功连接必须关闭
// （否则取消其余拨号会泄漏 fd）。
func TestTrustedDialHappyEyeballsClosesLateSuccess(t *testing.T) {
	old := lookupNetIP
	t.Cleanup(func() { lookupNetIP = old })
	lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"),
			netip.MustParseAddr("10.0.0.2"),
		}, nil
	}

	release := make(chan struct{})
	late := newCountingConn("10.0.0.1")
	win := newCountingConn("10.0.0.2")
	got, err := trustedDial(context.Background(), "tcp", "gw.internal:80",
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				<-release // 胜出者返回后才完成，模拟慢地址
				return late, nil
			}
			return win, nil
		})
	if err != nil || got != win {
		t.Fatalf("快速地址应胜出: conn=%v err=%v", got, err)
	}
	if late.isClosed() {
		t.Fatal("晚到连接在返回前不应被关闭")
	}

	close(release)
	select {
	case <-late.closedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("胜出后晚到的成功连接必须关闭（避免泄漏）")
	}
}

// countingConn 并发安全的可观测连接（RemoteAddr 用于建连后核对）。
type countingConn struct {
	fakeConn
	closedFlag atomic.Bool
	closedOnce sync.Once
	closedCh   chan struct{}
}

func newCountingConn(ip string) *countingConn {
	return &countingConn{
		fakeConn: fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip)}},
		closedCh: make(chan struct{}),
	}
}

func (c *countingConn) Close() error {
	c.closedFlag.Store(true)
	c.closedOnce.Do(func() { close(c.closedCh) })
	return nil
}

func (c *countingConn) isClosed() bool { return c.closedFlag.Load() }

// raceDial 空地址列表：fail-closed 报「无可拨号地址」，不调用拨号器。
func TestRaceDialEmptyIPs(t *testing.T) {
	_, err := raceDial(context.Background(), "tcp", "80", nil,
		func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("空地址列表不应发起拨号")
			return nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "无可拨号地址") {
		t.Fatalf("空地址列表应报错，实得 %v", err)
	}
}

// raceDial 全部失败：收满所有结果后返回最后收到的错误。
func TestRaceDialAllFail(t *testing.T) {
	errA, errB := errors.New("dial a failed"), errors.New("dial b failed")
	ips := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}
	_, err := raceDial(context.Background(), "tcp", "80", ips,
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				return nil, errA
			}
			return nil, errB
		})
	if err == nil {
		t.Fatal("全部失败应报错")
	}
	if !errors.Is(err, errA) && !errors.Is(err, errB) {
		t.Fatalf("应返回底层拨号错误，实得 %v", err)
	}
}

// raceDial 竞速中外部取消：拨号器阻塞到 ctx.Done 后返回 ctx.Err，结果必须是
// ctx.Err（取消优先于迟到结果），晚到的成功连接由收尾协程关闭。
func TestRaceDialCanceledDuringRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{}, 1)
	go func() {
		<-started
		cancel()
	}()
	ips := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}
	got, err := raceDial(ctx, "tcp", "80", ips,
		func(dctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-dctx.Done()
			return nil, dctx.Err()
		})
	if got != nil {
		t.Fatalf("取消时不得返回连接: %v", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实得 %v", err)
	}
}

// canceledOnlyContext 模拟「取消已被观测到、但 Done 尚未被 select 选中」的
// 窗口：Err 返回 Canceled，Done 永不就绪。用于确定性覆盖「取消优先于迟到
// 成功结果」的收口分支（真实 select 中两条 case 同时就绪时随机择一）。
type canceledOnlyContext struct{ context.Context }

func (canceledOnlyContext) Done() <-chan struct{} { return nil }
func (canceledOnlyContext) Err() error            { return context.Canceled }

// raceDial 取消与成功同时就绪：取消优先，成功连接自行关闭，不交给调用方。
func TestRaceDialCancelWinsOverSuccess(t *testing.T) {
	win := newCountingConn("10.0.0.2")
	got, err := raceDial(canceledOnlyContext{context.Background()}, "tcp", "80",
		[]netip.Addr{netip.MustParseAddr("10.0.0.2")},
		func(context.Context, string, string) (net.Conn, error) { return win, nil })
	if got != nil {
		t.Fatalf("取消时不得返回连接: %v", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("应返回 context.Canceled，实得 %v", err)
	}
	if !win.isClosed() {
		t.Fatal("取消优先时成功连接必须关闭")
	}
}

// raceDial 晚到成功：第一地址立即胜出；第二地址延迟返回的连接必须被关闭。
func TestRaceDialClosesLateSuccess(t *testing.T) {
	release := make(chan struct{})
	late := newCountingConn("10.0.0.1")
	win := newCountingConn("10.0.0.2")
	ips := []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}
	got, err := raceDial(context.Background(), "tcp", "80", ips,
		func(_ context.Context, _, addr string) (net.Conn, error) {
			if strings.HasPrefix(addr, "10.0.0.1:") {
				<-release // 胜出者返回后才完成，模拟慢地址
				return late, nil
			}
			return win, nil
		})
	if err != nil || got != win {
		t.Fatalf("快速地址应胜出: conn=%v err=%v", got, err)
	}
	if win.isClosed() {
		t.Fatal("胜出连接不得被关闭")
	}

	close(release)
	select {
	case <-late.closedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("胜出后晚到的成功连接必须关闭（避免泄漏）")
	}
}
